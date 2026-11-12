package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxAddresses       = 10
	maxLabelLength     = 30
	defaultLabel       = "Rumah"
	savedAddressFields = "id, label, recipient, phone, street, district, city, province, postal_code, main, created_at"
)

var errTooManyAddresses = fmt.Errorf("an account can keep at most %d addresses", maxAddresses)

// SavedAddress is one of an account's addresses. One of them is the main one.
type SavedAddress struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Address
	Main      bool      `json:"main"`
	CreatedAt time.Time `json:"created_at"`
}

// addressInput is an address as the customer fills it in. Main only makes it the main one; an
// address stops being main when another is chosen.
type addressInput struct {
	Label string `json:"label"`
	Address
	Main bool `json:"main"`
}

func (in *addressInput) validate() error {
	in.Label = strings.TrimSpace(in.Label)
	if err := checkText("label", in.Label, maxLabelLength); err != nil {
		return err
	}
	if strings.ContainsRune(in.Label, '\n') {
		return errors.New("label must be one line")
	}
	return in.Address.validate()
}

func scanSavedAddress(row pgx.Row) (SavedAddress, error) {
	var a SavedAddress
	err := row.Scan(&a.ID, &a.Label, &a.Recipient, &a.Phone, &a.Street, &a.District, &a.City, &a.Province, &a.PostalCode, &a.Main, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedAddress{}, errNoAddress
	}
	return a, err
}

// lockAccount keeps one account's address changes in line, so two at once can't pass the
// limit or leave two main addresses. An account deleted meanwhile gives errNotFound.
func lockAccount(ctx context.Context, tx pgx.Tx, userID int64) error {
	var one int
	err := tx.QueryRow(ctx, "SELECT 1 FROM users WHERE id = $1 FOR UPDATE", userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	return err
}

func (s *Store) Addresses(ctx context.Context, userID int64) ([]SavedAddress, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+savedAddressFields+" FROM user_addresses WHERE user_id = $1 ORDER BY main DESC, created_at, id", userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SavedAddress, error) { return scanSavedAddress(row) })
}

// Address is the main address, which checkout offers first.
func (s *Store) Address(ctx context.Context, userID int64) (Address, error) {
	a, err := scanSavedAddress(s.pool.QueryRow(ctx,
		"SELECT "+savedAddressFields+" FROM user_addresses WHERE user_id = $1 AND main", userID))
	return a.Address, err
}

// SaveAddress changes the main address, or adds one when the account has none. It is what
// shops from before addresses had labels still call.
func (s *Store) SaveAddress(ctx context.Context, userID int64, a Address) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE user_addresses SET recipient = $2, phone = $3, street = $4, district = $5, city = $6,
			     province = $7, postal_code = $8, updated_at = now()
			 WHERE user_id = $1 AND main`,
			userID, a.Recipient, a.Phone, a.Street, a.District, a.City, a.Province, a.PostalCode)
		if err != nil || tag.RowsAffected() == 1 {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO user_addresses (user_id, label, recipient, phone, street, district, city, province, postal_code, main)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true)`,
			userID, defaultLabel, a.Recipient, a.Phone, a.Street, a.District, a.City, a.Province, a.PostalCode)
		return err
	})
}

func unsetMain(ctx context.Context, tx pgx.Tx, userID int64) error {
	_, err := tx.Exec(ctx, "UPDATE user_addresses SET main = false, updated_at = now() WHERE user_id = $1 AND main", userID)
	return err
}

// AddAddress adds an address. The first one an account adds is its main one.
func (s *Store) AddAddress(ctx context.Context, userID int64, in addressInput) (saved SavedAddress, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM user_addresses WHERE user_id = $1", userID).Scan(&count); err != nil {
			return err
		}
		if count >= maxAddresses {
			return errTooManyAddresses
		}
		main := in.Main || count == 0
		if main {
			if err := unsetMain(ctx, tx, userID); err != nil {
				return err
			}
		}
		saved, err = scanSavedAddress(tx.QueryRow(ctx,
			`INSERT INTO user_addresses (user_id, label, recipient, phone, street, district, city, province, postal_code, main)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING `+savedAddressFields,
			userID, in.Label, in.Recipient, in.Phone, in.Street, in.District, in.City, in.Province, in.PostalCode, main))
		return err
	})
	return saved, err
}

func (s *Store) UpdateAddress(ctx context.Context, userID, id int64, in addressInput) (saved SavedAddress, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}
		if in.Main {
			var one int
			err := tx.QueryRow(ctx, "SELECT 1 FROM user_addresses WHERE id = $1 AND user_id = $2", id, userID).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNoAddress
			}
			if err != nil {
				return err
			}
			if err := unsetMain(ctx, tx, userID); err != nil {
				return err
			}
		}
		saved, err = scanSavedAddress(tx.QueryRow(ctx,
			`UPDATE user_addresses SET label = $3, recipient = $4, phone = $5, street = $6, district = $7, city = $8,
			     province = $9, postal_code = $10, main = main OR $11, updated_at = now()
			 WHERE id = $1 AND user_id = $2 RETURNING `+savedAddressFields,
			id, userID, in.Label, in.Recipient, in.Phone, in.Street, in.District, in.City, in.Province, in.PostalCode, in.Main))
		return err
	})
	return saved, err
}

func (s *Store) SetMainAddress(ctx context.Context, userID, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}
		if err := unsetMain(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE user_addresses SET main = true, updated_at = now() WHERE id = $1 AND user_id = $2", id, userID)
		if err == nil && tag.RowsAffected() == 0 {
			// Rolls back, so the old main address stays main.
			return errNoAddress
		}
		return err
	})
}

// DeleteAddress removes an address. When it was the main one, the oldest left takes its place.
func (s *Store) DeleteAddress(ctx context.Context, userID, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}
		var wasMain bool
		err := tx.QueryRow(ctx, "DELETE FROM user_addresses WHERE id = $1 AND user_id = $2 RETURNING main", id, userID).Scan(&wasMain)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoAddress
		}
		if err != nil || !wasMain {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE user_addresses SET main = true, updated_at = now()
			 WHERE id = (SELECT id FROM user_addresses WHERE user_id = $1 ORDER BY created_at, id LIMIT 1)`, userID)
		return err
	})
}

// addressList is what every change answers with, so the page can show the list as stored.
type addressList struct {
	Address   *SavedAddress  `json:"address,omitempty"`
	Addresses []SavedAddress `json:"addresses"`
	Limit     int            `json:"limit"`
}

func addAddressRoutes(mux *http.ServeMux, store *Store, tokenUser func(http.ResponseWriter, *http.Request) (int64, bool)) {
	answer := func(w http.ResponseWriter, r *http.Request, userID int64, status int, changed *SavedAddress) {
		list, err := store.Addresses(r.Context(), userID)
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, addressList{Address: changed, Addresses: list, Limit: maxAddresses})
	}
	// failed answers for the errors every change can give, and says whether there was one.
	failed := func(w http.ResponseWriter, err error) bool {
		switch {
		case err == nil:
			return false
		case errors.Is(err, errNotFound):
			// The account was deleted after the token was issued.
			writeUnauthorized(w, "the token is not valid")
		case errors.Is(err, errNoAddress):
			writeError(w, http.StatusNotFound, "address not found")
		case errors.Is(err, errTooManyAddresses):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeFailure(w, err)
		}
		return true
	}
	addressID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "address not found")
			return 0, false
		}
		return id, true
	}
	readInput := func(w http.ResponseWriter, r *http.Request) (addressInput, bool) {
		var in addressInput
		if !readJSON(w, r, &in) {
			return in, false
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return in, false
		}
		return in, true
	}

	mux.HandleFunc("GET /me/addresses", func(w http.ResponseWriter, r *http.Request) {
		if userID, ok := tokenUser(w, r); ok {
			answer(w, r, userID, http.StatusOK, nil)
		}
	})

	mux.HandleFunc("POST /me/addresses", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := tokenUser(w, r)
		if !ok {
			return
		}
		in, ok := readInput(w, r)
		if !ok {
			return
		}
		saved, err := store.AddAddress(r.Context(), userID, in)
		if failed(w, err) {
			return
		}
		answer(w, r, userID, http.StatusCreated, &saved)
	})

	mux.HandleFunc("PUT /me/addresses/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := tokenUser(w, r)
		if !ok {
			return
		}
		id, ok := addressID(w, r)
		if !ok {
			return
		}
		in, ok := readInput(w, r)
		if !ok {
			return
		}
		saved, err := store.UpdateAddress(r.Context(), userID, id, in)
		if failed(w, err) {
			return
		}
		answer(w, r, userID, http.StatusOK, &saved)
	})

	mux.HandleFunc("PUT /me/addresses/{id}/main", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := tokenUser(w, r)
		if !ok {
			return
		}
		id, ok := addressID(w, r)
		if !ok {
			return
		}
		if failed(w, store.SetMainAddress(r.Context(), userID, id)) {
			return
		}
		answer(w, r, userID, http.StatusOK, nil)
	})

	mux.HandleFunc("DELETE /me/addresses/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := tokenUser(w, r)
		if !ok {
			return
		}
		id, ok := addressID(w, r)
		if !ok {
			return
		}
		if failed(w, store.DeleteAddress(r.Context(), userID, id)) {
			return
		}
		answer(w, r, userID, http.StatusOK, nil)
	})
}
