package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the format image.DecodeConfig reads
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// The browser sends a square JPEG it has already made small, so this is generous.
	minPhotoSide = 64
	maxPhotoSide = 1024
	// Leaves room in maxBodyBytes for the base64 and the JSON around it.
	maxPhotoBytes = 46 << 10
)

var (
	errNoPhoto       = errors.New("no photo saved")
	errWrongPassword = errors.New("the current password is wrong")
)

// Profile is the account as its owner sees it on the account page.
type Profile struct {
	User
	Phone string `json:"phone"`
	// YYYY-MM-DD, or empty when not filled in.
	BirthDate string `json:"birth_date"`
	// "female", "male", or empty when not filled in.
	Gender string `json:"gender"`
	// Changes whenever the photo does, so the browser can keep a copy until then. 0 means none.
	PhotoVersion int64 `json:"photo_version"`
}

const profileColumns = userColumns + `, phone, COALESCE(to_char(birth_date, 'YYYY-MM-DD'), ''), gender,
	COALESCE((SELECT (extract(epoch FROM p.updated_at) * 1000)::bigint FROM user_photos p WHERE p.user_id = users.id), 0)`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	user, err := scanUser(row, &p.Phone, &p.BirthDate, &p.Gender, &p.PhotoVersion)
	p.User = user
	return p, err
}

func (s *Store) Profile(ctx context.Context, id int64) (Profile, error) {
	return scanProfile(s.pool.QueryRow(ctx, "SELECT "+profileColumns+" FROM users WHERE id = $1", id))
}

func (s *Store) UpdateProfile(ctx context.Context, id int64, in profileInput) (Profile, error) {
	var birthDate any
	if in.BirthDate != "" {
		birthDate = in.BirthDate
	}
	return scanProfile(s.pool.QueryRow(ctx,
		`UPDATE users SET name = $2, phone = $3, birth_date = $4, gender = $5, updated_at = now()
		 WHERE id = $1 RETURNING `+profileColumns,
		id, in.Name, in.Phone, birthDate, in.Gender))
}

func (s *Store) PasswordHash(ctx context.Context, id int64) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1", id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return hash, err
}

// SetPassword changes the hash only if it is still the one checked, so two changes at once
// can't both pass with the same old password.
func (s *Store) SetPassword(ctx context.Context, id int64, oldHash, newHash string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE users SET password_hash = $3, updated_at = now() WHERE id = $1 AND password_hash = $2",
		id, oldHash, newHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errWrongPassword
	}
	return nil
}

func (s *Store) Photo(ctx context.Context, id int64) ([]byte, error) {
	var image []byte
	err := s.pool.QueryRow(ctx, "SELECT image FROM user_photos WHERE user_id = $1", id).Scan(&image)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoPhoto
	}
	return image, err
}

func (s *Store) SavePhoto(ctx context.Context, id int64, image []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_photos (user_id, image) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET image = EXCLUDED.image, updated_at = now()`,
		id, image)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return errNotFound
	}
	return err
}

func (s *Store) DeletePhoto(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM user_photos WHERE user_id = $1", id)
	return err
}

type profileInput struct {
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	BirthDate string `json:"birth_date"`
	Gender    string `json:"gender"`
}

func (in *profileInput) validate(today time.Time) error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(in.Name) > maxNameLength:
		return fmt.Errorf("name must be at most %d characters", maxNameLength)
	case strings.ContainsRune(in.Name, 0):
		return errors.New("name must not contain NUL characters")
	}
	if in.Phone = cleanPhone(strings.TrimSpace(in.Phone)); in.Phone != "" && !mobilePhone.MatchString(in.Phone) {
		return errors.New("phone must be an Indonesian mobile number, such as 081234567890")
	}
	if in.BirthDate = strings.TrimSpace(in.BirthDate); in.BirthDate != "" {
		date, err := time.Parse(time.DateOnly, in.BirthDate)
		if err != nil {
			return errors.New("birth_date must be a date like 1995-04-18")
		}
		if date.Year() < 1900 || !date.Before(today) {
			return errors.New("birth_date must be between 1900 and today")
		}
	}
	switch in.Gender {
	case "", "female", "male":
	default:
		return errors.New(`gender must be "female", "male" or empty`)
	}
	return nil
}

type passwordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type photoInput struct {
	// A square JPEG, base64-encoded.
	Image string `json:"image"`
}

// checkPhoto makes sure the bytes are a square JPEG of a sensible size, so nothing else can be
// stored and later served as a picture.
func checkPhoto(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("image must be base64")
	}
	if len(data) > maxPhotoBytes {
		return nil, fmt.Errorf("image must be at most %d KB", maxPhotoBytes>>10)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		return nil, errors.New("image must be a JPEG")
	}
	if config.Width != config.Height || config.Width < minPhotoSide || config.Width > maxPhotoSide {
		return nil, fmt.Errorf("image must be square, %d to %d pixels wide", minPhotoSide, maxPhotoSide)
	}
	return data, nil
}

func addProfileRoutes(mux *http.ServeMux, store *Store, passwords *Passwords, tokenUser func(http.ResponseWriter, *http.Request) (int64, bool)) {
	// gone answers for an account deleted after its token was issued.
	gone := func(w http.ResponseWriter, err error) bool {
		if errors.Is(err, errNotFound) {
			writeUnauthorized(w, "the token is not valid")
			return true
		}
		if err != nil {
			writeFailure(w, err)
			return true
		}
		return false
	}

	mux.HandleFunc("PUT /me", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		var in profileInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(time.Now()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		profile, err := store.UpdateProfile(r.Context(), id, in)
		if gone(w, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, profile)
	})

	mux.HandleFunc("PUT /me/password", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		var in passwordInput
		if !readJSON(w, r, &in) {
			return
		}
		if in.CurrentPassword == "" {
			writeError(w, http.StatusBadRequest, "current_password is required")
			return
		}
		if err := checkPassword(in.NewPassword); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := store.PasswordHash(r.Context(), id)
		if gone(w, err) {
			return
		}
		waitCtx, cancel := context.WithTimeout(r.Context(), passwordWait)
		defer cancel()
		ok, err = passwords.Verify(waitCtx, hash, in.CurrentPassword)
		if err != nil {
			writeFailure(w, err)
			return
		}
		// 403, not 401: the session is fine, so the shop must not log the customer out.
		if !ok {
			writeError(w, http.StatusForbidden, errWrongPassword.Error())
			return
		}
		newHash, err := passwords.Hash(waitCtx, in.NewPassword)
		if err != nil {
			writeFailure(w, err)
			return
		}
		err = store.SetPassword(r.Context(), id, hash, newHash)
		if errors.Is(err, errWrongPassword) {
			writeError(w, http.StatusConflict, "the password was changed meanwhile")
			return
		}
		if gone(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /me/photo", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		image, err := store.Photo(r.Context(), id)
		if errors.Is(err, errNoPhoto) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(image)))
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Write(image)
	})

	mux.HandleFunc("PUT /me/photo", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		var in photoInput
		if !readJSON(w, r, &in) {
			return
		}
		image, err := checkPhoto(in.Image)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if gone(w, store.SavePhoto(r.Context(), id, image)) {
			return
		}
		profile, err := store.Profile(r.Context(), id)
		if gone(w, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, profile)
	})

	mux.HandleFunc("DELETE /me/photo", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		if err := store.DeletePhoto(r.Context(), id); err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
