package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var builtinThemes = []string{
	"github-dark",
	"github-light",
	"dracula",
	"nord",
	"solarized",
}

// FileEntry represents a markdown file in the root directory.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	ModTime time.Time `json:"mod_time"`
}

// API holds the configuration for the HTTP API handlers.
type API struct {
	root       string
	hub        *Hub
	targetFile string // non-empty in single-file mode (relative path)
}

// NewAPI creates a new API with the given root directory and hub.
// If targetFile is non-empty, HandleFiles returns only that file.
func NewAPI(root string, hub *Hub, targetFile string) *API {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &API{root: abs, hub: hub, targetFile: targetFile}
}

// safePath resolves the given relative path within root and verifies it does
// not escape the root directory. Returns the absolute path or an error.
func (a *API) safePath(rel string) (string, error) {
	// Clean and resolve to absolute path.
	abs := filepath.Join(a.root, filepath.FromSlash(rel))
	abs = filepath.Clean(abs)

	// Ensure the resolved path is within root.
	rootWithSep := a.root
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	if abs != a.root && !strings.HasPrefix(abs, rootWithSep) {
		return "", os.ErrPermission
	}
	return abs, nil
}

// HandleFile serves GET /api/file?path=X.
func (a *API) HandleFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".md" && ext != ".markdown" {
		http.Error(w, "only markdown files are supported", http.StatusForbidden)
		return
	}

	abs, err := a.safePath(rel)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// HandleFiles serves GET /api/files, returning JSON array of markdown FileEntry.
func (a *API) HandleFiles(w http.ResponseWriter, r *http.Request) {
	var entries []FileEntry

	if a.targetFile != "" {
		// Single-file mode: return only the target file
		abs := filepath.Join(a.root, a.targetFile)
		info, err := os.Stat(abs)
		if err == nil {
			entries = append(entries, FileEntry{
				Name:    a.targetFile,
				Path:    a.targetFile,
				ModTime: info.ModTime(),
			})
		}
	} else {
		err := filepath.Walk(a.root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".md" && ext != ".markdown" {
				return nil
			}
			rel, relErr := filepath.Rel(a.root, path)
			if relErr != nil {
				return nil
			}
			entries = append(entries, FileEntry{
				Name:    rel,
				Path:    rel,
				ModTime: info.ModTime(),
			})
			return nil
		})
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if entries == nil {
		entries = []FileEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(entries)
}

// HandleThemes serves GET /api/themes, returning built-in plus any custom themes.
func (a *API) HandleThemes(w http.ResponseWriter, r *http.Request) {
	themes := make([]string, len(builtinThemes))
	copy(themes, builtinThemes)

	customDir := filepath.Join(os.Getenv("HOME"), ".config", "spec-viewer", "themes")
	entries, err := os.ReadDir(customDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
				themes = append(themes, name)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(themes)
}

// HandleOpen serves POST /api/open with {"path":"..."}.
func (a *API) HandleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(req.Path))
	if ext != ".md" && ext != ".markdown" {
		http.Error(w, "only markdown files are supported", http.StatusBadRequest)
		return
	}

	abs, err := a.safePath(req.Path)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if _, err := os.Stat(abs); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Broadcast navigate event.
	rel, _ := filepath.Rel(a.root, abs)
	msg, _ := json.Marshal(map[string]string{
		"type": "navigate",
		"path": rel,
	})
	a.hub.Broadcast(msg)

	url := "/view?path=" + rel
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"url": url})
}
