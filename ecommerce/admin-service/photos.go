package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	maxPhotoBytes     = 5 << 20
	productPhotoSlots = 4
	maxPhotoSide      = 10000
)

var (
	photoSlot = regexp.MustCompile(`^[1-4]$`)
	// The shop finds a category's photo by its name in lowercase with dashes for spaces,
	// and only serves names like these.
	categoryPhotoName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	photoExtensions   = []string{".jpg", ".jpeg", ".png", ".webp"}
	photoTypes        = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}

	errNotAPhoto        = errors.New("the file is not a JPEG, PNG or WEBP photo")
	errPhotoName        = errors.New("this category name can't name a photo; use letters, numbers and spaces")
	errCategoryNotFound = errors.New("category not found")
)

// photoStore writes the photos the shop (web) serves from the same folder on AldiServer:
// products/<id>/1.jpg to 4.jpg, and categories/<name>.jpg. One lock keeps two changes to
// one product from mixing; the folder is small and changes rarely.
type photoStore struct {
	dir string
	mu  sync.Mutex
}

// categorySlug is how the shop turns a category's name into its photo's name.
func categorySlug(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), "-")
}

// checkPhoto looks at the content, not the name or the header the browser sent, and
// returns the extension to store it under.
func checkPhoto(data []byte) (string, error) {
	extension, ok := photoTypes[http.DetectContentType(data)]
	if !ok {
		return "", errNotAPhoto
	}
	if extension == ".webp" {
		return extension, nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxPhotoSide || config.Height > maxPhotoSide {
		return "", errNotAPhoto
	}
	return extension, nil
}

// find returns the regular file named base with a photo extension, or "".
func find(dir, base string) string {
	for _, extension := range photoExtensions {
		path := filepath.Join(dir, base+extension)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// removeAll removes base under every photo extension except keep.
func removeAll(dir, base, keep string) error {
	for _, extension := range photoExtensions {
		path := filepath.Join(dir, base+extension)
		if path == keep {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// write puts data in place in one step, so the shop never serves half a photo.
func write(dir, base, extension string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	target := filepath.Join(dir, base+extension)
	if err := os.Rename(temp.Name(), target); err != nil {
		return err
	}
	return removeAll(dir, base, target)
}

// photoURL is the shop's address for a photo, with the same version it gives, so a
// replaced photo gets a new address past Cloudflare's cache.
func photoURL(prefix, path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	version := strconv.FormatInt(info.ModTime().Unix(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
	return prefix + filepath.Base(path) + "?v=" + version
}

func (p *photoStore) productDir(id string) string {
	return filepath.Join(p.dir, "products", id)
}

// productPhotos returns the four places of a product, empty where there's no photo.
func (p *photoStore) productPhotos(id string) []string {
	slots := make([]string, productPhotoSlots)
	for i := range slots {
		if path := find(p.productDir(id), strconv.Itoa(i+1)); path != "" {
			slots[i] = photoURL("/photos/products/"+id+"/", path)
		}
	}
	return slots
}

func (p *photoStore) allProductPhotos() (map[string][]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(p.dir, "products"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	all := map[string][]string{}
	for _, entry := range entries {
		if entry.IsDir() && numericID.MatchString(entry.Name()) {
			all[entry.Name()] = p.productPhotos(entry.Name())
		}
	}
	return all, nil
}

func (p *photoStore) saveProductPhoto(id, slot string, data []byte) ([]string, error) {
	extension, err := checkPhoto(data)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := write(p.productDir(id), slot, extension, data); err != nil {
		return nil, err
	}
	return p.productPhotos(id), nil
}

func (p *photoStore) deleteProductPhoto(id, slot string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := p.productDir(id)
	if err := removeAll(dir, slot, ""); err != nil {
		return nil, err
	}
	// An empty folder goes too; Remove refuses one that still has photos.
	os.Remove(dir)
	return p.productPhotos(id), nil
}

// makeFirst swaps a photo with the first one, which the shop shows on the product's card.
func (p *photoStore) makeFirst(id, slot string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := p.productDir(id)
	chosen := find(dir, slot)
	if chosen == "" {
		return nil, fs.ErrNotExist
	}
	if slot != "1" {
		first := find(dir, "1")
		temp := filepath.Join(dir, ".swap"+filepath.Ext(chosen))
		if err := os.Rename(chosen, temp); err != nil {
			return nil, err
		}
		if first != "" {
			if err := os.Rename(first, filepath.Join(dir, slot+filepath.Ext(first))); err != nil {
				return nil, err
			}
		}
		if err := os.Rename(temp, filepath.Join(dir, "1"+filepath.Ext(chosen))); err != nil {
			return nil, err
		}
	}
	return p.productPhotos(id), nil
}

func (p *photoStore) removeProduct(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return os.RemoveAll(p.productDir(id))
}

func (p *photoStore) categoryDir() string {
	return filepath.Join(p.dir, "categories")
}

func (p *photoStore) categoryPhoto(name string) string {
	slug := categorySlug(name)
	if !categoryPhotoName.MatchString(slug) {
		return ""
	}
	if path := find(p.categoryDir(), slug); path != "" {
		return photoURL("/photos/categories/", path)
	}
	return ""
}

func (p *photoStore) saveCategoryPhoto(name string, data []byte) (string, error) {
	slug := categorySlug(name)
	if !categoryPhotoName.MatchString(slug) {
		return "", errPhotoName
	}
	extension, err := checkPhoto(data)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := write(p.categoryDir(), slug, extension, data); err != nil {
		return "", err
	}
	return p.categoryPhoto(name), nil
}

func (p *photoStore) deleteCategoryPhoto(name string) error {
	slug := categorySlug(name)
	if !categoryPhotoName.MatchString(slug) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return removeAll(p.categoryDir(), slug, "")
}

// renameCategoryPhoto keeps a renamed category's photo. A photo already under the new
// name, such as from a category deleted earlier, is kept rather than overwritten.
func (p *photoStore) renameCategoryPhoto(oldName, newName string) error {
	from, to := categorySlug(oldName), categorySlug(newName)
	if from == to || !categoryPhotoName.MatchString(from) || !categoryPhotoName.MatchString(to) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := find(p.categoryDir(), from)
	if old == "" || find(p.categoryDir(), to) != "" {
		return nil
	}
	return os.Rename(old, filepath.Join(p.categoryDir(), to+filepath.Ext(old)))
}

// categoryByID asks catalog-service for the category's current name.
func (a *admin) categoryByID(ctx context.Context, id string) (string, error) {
	var out struct {
		Categories []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"categories"`
	}
	if err := a.call(ctx, http.MethodGet, a.catalogURL+"/categories", "", nil, &out); err != nil {
		return "", err
	}
	for _, category := range out.Categories {
		if strconv.FormatInt(category.ID, 10) == id {
			return category.Name, nil
		}
	}
	return "", errCategoryNotFound
}

func readPhoto(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPhotoBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a photo can be at most %d MB", maxPhotoBytes>>20))
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, "could not read the photo")
		return nil, false
	case len(data) == 0:
		writeError(w, http.StatusBadRequest, errNotAPhoto.Error())
		return nil, false
	}
	return data, true
}

func (a *admin) photoFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotAPhoto), errors.Is(err, errPhotoName):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errCategoryNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "photo not found")
	default:
		var answer *upstreamError
		if errors.As(err, &answer) {
			a.fail(w, err)
			return
		}
		slog.Error("changing a photo failed", "error", err)
		writeError(w, http.StatusInternalServerError, "the photo could not be changed")
	}
}

// addPhotoRoutes lets the admin pages put up to four photos on a product and one on a
// category. Each change is logged with the admin who made it.
func (a *admin) addPhotoRoutes(mux *http.ServeMux) {
	productSlot := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		id, slot := r.PathValue("id"), r.PathValue("slot")
		if !numericID.MatchString(id) || !photoSlot.MatchString(slot) {
			writeError(w, http.StatusNotFound, "not found")
			return "", "", false
		}
		return id, slot, true
	}
	writePhotos := func(w http.ResponseWriter, photos []string) {
		writeJSON(w, http.StatusOK, map[string][]string{"photos": photos})
	}

	mux.HandleFunc("GET /photos/products", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		all, err := a.photos.allProductPhotos()
		if err != nil {
			a.photoFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"photos": all})
	}))
	mux.HandleFunc("PUT /photos/products/{id}/{slot}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, slot, ok := productSlot(w, r)
		if !ok {
			return
		}
		data, ok := readPhoto(w, r)
		if !ok {
			return
		}
		// Only a product that exists gets a folder.
		var product map[string]any
		if err := a.call(r.Context(), http.MethodGet, a.catalogURL+"/products/"+id, "", nil, &product); err != nil {
			a.fail(w, err)
			return
		}
		photos, err := a.photos.saveProductPhoto(id, slot, data)
		if err != nil {
			a.photoFail(w, err)
			return
		}
		slog.Info("product photo saved", "admin_id", me.ID, "product_id", id, "slot", slot, "bytes", len(data))
		writePhotos(w, photos)
	}))
	mux.HandleFunc("DELETE /photos/products/{id}/{slot}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, slot, ok := productSlot(w, r)
		if !ok {
			return
		}
		photos, err := a.photos.deleteProductPhoto(id, slot)
		if err != nil {
			a.photoFail(w, err)
			return
		}
		slog.Info("product photo deleted", "admin_id", me.ID, "product_id", id, "slot", slot)
		writePhotos(w, photos)
	}))
	mux.HandleFunc("POST /photos/products/{id}/{slot}/first", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, slot, ok := productSlot(w, r)
		if !ok {
			return
		}
		photos, err := a.photos.makeFirst(id, slot)
		if err != nil {
			a.photoFail(w, err)
			return
		}
		slog.Info("product photo made first", "admin_id", me.ID, "product_id", id, "slot", slot)
		writePhotos(w, photos)
	}))

	mux.HandleFunc("GET /photos/categories", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		var out struct {
			Categories []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"categories"`
		}
		if err := a.call(r.Context(), http.MethodGet, a.catalogURL+"/categories", "", nil, &out); err != nil {
			a.fail(w, err)
			return
		}
		photos := map[string]string{}
		for _, category := range out.Categories {
			if url := a.photos.categoryPhoto(category.Name); url != "" {
				photos[strconv.FormatInt(category.ID, 10)] = url
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"photos": photos})
	}))
	mux.HandleFunc("PUT /photos/categories/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		data, ok := readPhoto(w, r)
		if !ok {
			return
		}
		name, err := a.categoryByID(r.Context(), id)
		if err != nil {
			a.photoFail(w, err)
			return
		}
		url, err := a.photos.saveCategoryPhoto(name, data)
		if err != nil {
			a.photoFail(w, err)
			return
		}
		slog.Info("category photo saved", "admin_id", me.ID, "category_id", id, "bytes", len(data))
		writeJSON(w, http.StatusOK, map[string]string{"photo": url})
	}))
	mux.HandleFunc("DELETE /photos/categories/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		name, err := a.categoryByID(r.Context(), id)
		if err == nil {
			err = a.photos.deleteCategoryPhoto(name)
		}
		if err != nil {
			a.photoFail(w, err)
			return
		}
		slog.Info("category photo deleted", "admin_id", me.ID, "category_id", id)
		w.WriteHeader(http.StatusNoContent)
	}))
}
