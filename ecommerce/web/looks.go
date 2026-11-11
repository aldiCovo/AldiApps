package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Shop the look: admin-service keeps the looks in looks/looks.json beside their photos.
// The shop shows one: the most recently changed that's turned on and has a photo and products.

type lookItem struct {
	ProductID int64   `json:"product_id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
}

type storedLook struct {
	Eyebrow   string     `json:"eyebrow"`
	Title     string     `json:"title"`
	Accent    string     `json:"accent"`
	Active    bool       `json:"active"`
	Items     []lookItem `json:"items"`
	Photo     string     `json:"photo"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type shownLook struct {
	Eyebrow  string     `json:"eyebrow"`
	Title    string     `json:"title"`
	Accent   string     `json:"accent"`
	PhotoURL string     `json:"photo_url"`
	Items    []lookItem `json:"items"`
}

func currentLook(dir string) *shownLook {
	data, err := os.ReadFile(filepath.Join(dir, "looks.json"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Error("reading looks failed", "error", err)
		}
		return nil
	}
	var stored struct {
		Looks []storedLook `json:"looks"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		slog.Error("reading looks failed", "error", err)
		return nil
	}
	var best *storedLook
	var photo fs.FileInfo
	for i := range stored.Looks {
		look := &stored.Looks[i]
		if !look.Active || len(look.Items) == 0 || !photoFile.MatchString(look.Photo) {
			continue
		}
		if best != nil && !look.UpdatedAt.After(best.UpdatedAt) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, look.Photo))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		best, photo = look, info
	}
	if best == nil {
		return nil
	}
	return &shownLook{
		Eyebrow: best.Eyebrow, Title: best.Title, Accent: best.Accent, Items: best.Items,
		PhotoURL: "/photos/looks/" + best.Photo + "?v=" + photoVersion(photo),
	}
}

func addLookRoutes(mux *http.ServeMux, photosDir string) {
	dir := filepath.Join(photosDir, "looks")
	mux.HandleFunc("GET /api/look", func(w http.ResponseWriter, r *http.Request) {
		writePhotoList(w, map[string]any{"look": currentLook(dir)})
	})
	mux.HandleFunc("GET /photos/looks/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !photoFile.MatchString(r.PathValue("name")) {
			http.NotFound(w, r)
			return
		}
		servePhoto(w, r, filepath.Join(dir, r.PathValue("name")))
	})
}
