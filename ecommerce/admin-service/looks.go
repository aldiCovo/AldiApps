package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Shop the look: a photo of an outfit with a point on each product worn, which the shop
// shows on its homepage. They're kept as one file beside the photos, which web reads, since
// there are only a few and only the admins change them.
const (
	maxLooks        = 20
	maxLookItems    = 6
	maxLookTitle    = 80
	maxLookEyebrow  = 60
	looksFile       = "looks.json"
	lookPhotoPrefix = "/photos/looks/"
)

var errLookNotFound = errors.New("look not found")

// LookItem is a product in the photo, at X and Y percent from the photo's top left.
type LookItem struct {
	ProductID int64   `json:"product_id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
}

type Look struct {
	ID int64 `json:"id"`
	// Eyebrow is the small line above the title, Title the heading, and Accent an optional
	// second part of it in italics.
	Eyebrow string     `json:"eyebrow"`
	Title   string     `json:"title"`
	Accent  string     `json:"accent"`
	Active  bool       `json:"active"`
	Items   []LookItem `json:"items"`
	// Photo is the file's name in the looks folder, empty until one is uploaded.
	Photo     string    `json:"photo"`
	UpdatedAt time.Time `json:"updated_at"`
}

type looksData struct {
	NextID int64  `json:"next_id"`
	Looks  []Look `json:"looks"`
}

type lookInput struct {
	Eyebrow string     `json:"eyebrow"`
	Title   string     `json:"title"`
	Accent  string     `json:"accent"`
	Active  bool       `json:"active"`
	Items   []LookItem `json:"items"`
}

func (in *lookInput) validate() error {
	in.Eyebrow, in.Title, in.Accent = strings.TrimSpace(in.Eyebrow), strings.TrimSpace(in.Title), strings.TrimSpace(in.Accent)
	switch {
	case in.Title == "" || utf8.RuneCountInString(in.Title) > maxLookTitle:
		return fmt.Errorf("title must be 1 to %d characters", maxLookTitle)
	case utf8.RuneCountInString(in.Accent) > maxLookTitle:
		return fmt.Errorf("accent must be at most %d characters", maxLookTitle)
	case utf8.RuneCountInString(in.Eyebrow) > maxLookEyebrow:
		return fmt.Errorf("eyebrow must be at most %d characters", maxLookEyebrow)
	case len(in.Items) > maxLookItems:
		return fmt.Errorf("a look has at most %d products", maxLookItems)
	}
	seen := map[int64]bool{}
	for i, item := range in.Items {
		switch {
		case item.ProductID <= 0:
			return errors.New("each product_id must be a positive integer")
		case seen[item.ProductID]:
			return errors.New("each product can only appear once")
		case math.IsNaN(item.X) || math.IsNaN(item.Y) || item.X < 0 || item.X > 100 || item.Y < 0 || item.Y > 100:
			return errors.New("x and y must be between 0 and 100")
		}
		seen[item.ProductID] = true
		// One decimal is finer than a finger can point.
		in.Items[i].X, in.Items[i].Y = math.Round(item.X*10)/10, math.Round(item.Y*10)/10
	}
	if in.Items == nil {
		in.Items = []LookItem{}
	}
	return nil
}

func (p *photoStore) looksDir() string {
	return filepath.Join(p.dir, "looks")
}

// readLooks needs p.mu held.
func (p *photoStore) readLooks() (looksData, error) {
	data, err := os.ReadFile(filepath.Join(p.looksDir(), looksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return looksData{NextID: 1, Looks: []Look{}}, nil
	}
	if err != nil {
		return looksData{}, err
	}
	var out looksData
	if err := json.Unmarshal(data, &out); err != nil {
		return looksData{}, err
	}
	if out.Looks == nil {
		out.Looks = []Look{}
	}
	return out, nil
}

// writeLooks needs p.mu held. It replaces the file in one step, so web never reads half.
func (p *photoStore) writeLooks(data looksData) error {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return write(p.looksDir(), strings.TrimSuffix(looksFile, ".json"), ".json", body)
}

// lookView is a look as the admin pages see it, with its photo's address.
type lookView struct {
	Look
	PhotoURL string `json:"photo_url"`
}

func (p *photoStore) view(look Look) lookView {
	v := lookView{Look: look}
	if look.Photo != "" {
		v.PhotoURL = photoURL(lookPhotoPrefix, filepath.Join(p.looksDir(), look.Photo))
	}
	return v
}

// changeLook runs change on look id under the lock and saves. change may delete it by
// returning false.
func (p *photoStore) changeLook(id int64, change func(*Look) (keep bool, err error)) (Look, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, err := p.readLooks()
	if err != nil {
		return Look{}, err
	}
	for i := range data.Looks {
		if data.Looks[i].ID != id {
			continue
		}
		keep, err := change(&data.Looks[i])
		if err != nil {
			return Look{}, err
		}
		look := data.Looks[i]
		if keep {
			data.Looks[i].UpdatedAt = time.Now().UTC()
			look = data.Looks[i]
		} else {
			data.Looks = append(data.Looks[:i], data.Looks[i+1:]...)
		}
		return look, p.writeLooks(data)
	}
	return Look{}, errLookNotFound
}

var errTooManyLooks = fmt.Errorf("at most %d looks can be kept", maxLooks)

func (a *admin) addLookRoutes(mux *http.ServeMux) {
	lookID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errLookNotFound.Error())
			return 0, false
		}
		return id, true
	}
	// Only products that exist can be pointed at.
	readInput := func(w http.ResponseWriter, r *http.Request) (lookInput, bool) {
		var in lookInput
		if !readJSON(w, r, &in) {
			return in, false
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return in, false
		}
		for _, item := range in.Items {
			var product map[string]any
			err := a.call(r.Context(), http.MethodGet, a.catalogURL+"/products/"+strconv.FormatInt(item.ProductID, 10), "", nil, &product)
			if err != nil {
				a.fail(w, err)
				return in, false
			}
		}
		return in, true
	}
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, errLookNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, errTooManyLooks):
			writeError(w, http.StatusConflict, err.Error())
		default:
			a.photoFail(w, err)
		}
	}

	mux.HandleFunc("GET /looks", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		a.photos.mu.Lock()
		data, err := a.photos.readLooks()
		a.photos.mu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		views := make([]lookView, len(data.Looks))
		for i, look := range data.Looks {
			views[i] = a.photos.view(look)
		}
		writeJSON(w, http.StatusOK, map[string]any{"looks": views})
	}))

	mux.HandleFunc("POST /looks", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		in, ok := readInput(w, r)
		if !ok {
			return
		}
		a.photos.mu.Lock()
		data, err := a.photos.readLooks()
		var look Look
		if err == nil && len(data.Looks) >= maxLooks {
			err = errTooManyLooks
		}
		if err == nil {
			look = Look{ID: data.NextID, Eyebrow: in.Eyebrow, Title: in.Title, Accent: in.Accent, Active: in.Active, Items: in.Items, UpdatedAt: time.Now().UTC()}
			data.NextID++
			data.Looks = append(data.Looks, look)
			err = a.photos.writeLooks(data)
		}
		a.photos.mu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		slog.Info("look created", "admin_id", me.ID, "look_id", look.ID)
		writeJSON(w, http.StatusCreated, a.photos.view(look))
	}))

	mux.HandleFunc("PUT /looks/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := lookID(w, r)
		if !ok {
			return
		}
		in, ok := readInput(w, r)
		if !ok {
			return
		}
		look, err := a.photos.changeLook(id, func(l *Look) (bool, error) {
			l.Eyebrow, l.Title, l.Accent, l.Active, l.Items = in.Eyebrow, in.Title, in.Accent, in.Active, in.Items
			return true, nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		slog.Info("look changed", "admin_id", me.ID, "look_id", id)
		writeJSON(w, http.StatusOK, a.photos.view(look))
	}))

	mux.HandleFunc("DELETE /looks/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := lookID(w, r)
		if !ok {
			return
		}
		_, err := a.photos.changeLook(id, func(l *Look) (bool, error) {
			return false, removeAll(a.photos.looksDir(), strconv.FormatInt(l.ID, 10), "")
		})
		if err != nil {
			fail(w, err)
			return
		}
		slog.Info("look deleted", "admin_id", me.ID, "look_id", id)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("PUT /looks/{id}/photo", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := lookID(w, r)
		if !ok {
			return
		}
		data, ok := readPhoto(w, r)
		if !ok {
			return
		}
		extension, err := checkPhoto(data)
		if err != nil {
			fail(w, err)
			return
		}
		look, err := a.photos.changeLook(id, func(l *Look) (bool, error) {
			base := strconv.FormatInt(l.ID, 10)
			if err := write(a.photos.looksDir(), base, extension, data); err != nil {
				return false, err
			}
			l.Photo = base + extension
			return true, nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		slog.Info("look photo saved", "admin_id", me.ID, "look_id", id, "bytes", len(data))
		writeJSON(w, http.StatusOK, a.photos.view(look))
	}))
}
