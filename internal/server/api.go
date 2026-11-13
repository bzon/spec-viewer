package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bzon/spec-viewer/internal/watcher"
)

// OpenTokenHeader carries the secret from the lock file on /api/open requests.
const OpenTokenHeader = "X-Spec-Viewer-Token"

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

// API holds the HTTP API handlers and the workspaces they serve.
type API struct {
	reg         *registry
	annotations *annotationStore

	// Hand-off configuration, set by the Server. With no token, /api/open
	// refuses every request.
	openToken  string
	version    string
	listenPort int
	// exposed marks a server bound to a non-loopback host: it never accepts
	// hand-offs, so folders other invocations choose are not served to the network.
	exposed bool
}

// SetHandoff sets the secret that /api/open requires in X-Spec-Viewer-Token and
// the version reported by /api/health.
func (a *API) SetHandoff(token, version string) {
	a.openToken = token
	a.version = version
}

// SetExposed marks the server as reachable from the network, which makes
// /api/open refuse every request.
func (a *API) SetExposed(exposed bool) {
	a.exposed = exposed
}

// SetListenPort records the port /api/open uses to recognise its own origin.
func (a *API) SetListenPort(port int) {
	a.listenPort = port
}

// NewAPI creates a new API whose default workspace serves root with the given hub.
// If targetFile is non-empty, the default workspace is in single-file mode.
func NewAPI(root string, hub *Hub, targetFile string) *API {
	return NewAPIWithAnnotations(root, hub, targetFile, defaultAnnotationsPath())
}

// NewAPIWithAnnotations creates an API using a specific annotation file.
func NewAPIWithAnnotations(root string, hub *Hub, targetFile, annotationsPath string) *API {
	return &API{
		reg:         newRegistry(newWorkspace(root, targetFile, hub)),
		annotations: &annotationStore{path: annotationsPath},
	}
}

// EnableWatching starts a file watcher for the default workspace and for every
// workspace created later. Without it nothing watches files. A watcher that
// cannot start is returned as an error for the default workspace and logged
// for later ones; the workspace is served without live reload either way.
func (a *API) EnableWatching() error {
	budget := a.reg.watchBudget
	a.reg.startWatcher = func(ws *workspace) (io.Closer, error) { return startWorkspaceWatcher(ws, budget) }
	def := a.reg.defaultWS
	w, err := startWorkspaceWatcher(def, budget)
	if err != nil {
		return err
	}
	def.watcher = w
	return nil
}

// Close stops every workspace's watcher and disconnects its clients.
func (a *API) Close() {
	a.reg.closeAll()
}

// startWorkspaceWatcher broadcasts a reload to the workspace's hub whenever a
// markdown file under it changes.
// The watcher's entries are charged to the process-wide budget.
func startWorkspaceWatcher(ws *workspace, budget *watcher.Budget) (io.Closer, error) {
	w, err := watcher.NewWithBudget(ws.watchPath(), func(changedPath string) {
		// An empty path still triggers a reload (file list refresh) without
		// naming a document the browser could mistake for its current file.
		rel, _ := RelativeDocumentPath(ws.realRoot, changedPath)
		msg, _ := json.Marshal(map[string]string{"type": "reload", "path": rel})
		ws.hub.Broadcast(msg)
	}, budget)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// lookupWorkspace resolves the workspace named by the ws query parameter (the
// default workspace when absent). It writes a 404 and returns false for an
// unknown id.
func (a *API) lookupWorkspace(w http.ResponseWriter, r *http.Request) (*workspace, bool) {
	ws, ok := a.reg.lookup(r.URL.Query().Get("ws"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown workspace")
		return nil, false
	}
	return ws, true
}

// workspaceFor is lookupWorkspace for the data endpoints: it also answers 404
// when the workspace's folder has been deleted since it was opened.
func (a *API) workspaceFor(w http.ResponseWriter, r *http.Request) (*workspace, bool) {
	ws, ok := a.lookupWorkspace(w, r)
	if !ok {
		return nil, false
	}
	if ws.rootMissing() {
		writeJSONError(w, http.StatusNotFound, "workspace folder no longer exists")
		return nil, false
	}
	return ws, true
}

// HandleFile serves GET /api/file?path=X.
func (a *API) HandleFile(w http.ResponseWriter, r *http.Request) {
	ws, ok := a.workspaceFor(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".md" && ext != ".markdown" {
		http.Error(w, "only markdown files are supported", http.StatusForbidden)
		return
	}

	if !ws.allowsDocument(rel) {
		writeJSONError(w, http.StatusForbidden, "this workspace serves a single file only")
		return
	}

	abs, err := ws.resolveInside(rel)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if isMissing(err) {
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
	ws, ok := a.workspaceFor(w, r)
	if !ok {
		return
	}
	var entries []FileEntry

	if ws.target != "" {
		// Single-file mode: return only the target file
		info, err := os.Stat(filepath.Join(ws.root, ws.target))
		_, insideErr := ws.resolveInside(ws.target)
		if err == nil && insideErr == nil {
			entries = append(entries, FileEntry{
				Name:    ws.target,
				Path:    ws.target,
				ModTime: info.ModTime(),
			})
		}
	} else {
		// Walk the resolved root: filepath.Walk does not descend into a root
		// that is itself a symlink. Symlinked directories inside the root are
		// not followed (no cycles, no escaping the root). A symlinked markdown
		// file is listed only when it resolves to a regular file under the
		// resolved root, which is exactly what HandleFile will serve.
		err := filepath.Walk(ws.realRoot, func(path string, info os.FileInfo, err error) error {
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
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, evalErr := filepath.EvalSymlinks(path)
				if evalErr != nil {
					return nil
				}
				if _, inside := relativeInside(ws.realRoot, resolved); !inside {
					return nil
				}
				target, statErr := os.Stat(resolved)
				if statErr != nil || !target.Mode().IsRegular() {
					return nil
				}
				info = target
			}
			rel, relErr := filepath.Rel(ws.realRoot, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
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

// HandleHealth serves GET /api/health so a caller can confirm that a port
// belongs to a live spec-viewer and learn its pid.
func (a *API) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app":        "spec-viewer",
		"version":    a.version,
		"pid":        os.Getpid(),
		"workspaces": a.reg.count(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// originAllowed reports whether the Origin header is absent or names this
// server on a loopback host and its own port.
func (a *API) originAllowed(r *http.Request) bool {
	return originIsOwn(r.Header.Get("Origin"), a.listenPort)
}

// resolveOpenPath maps a requested path (absolute, or relative to the default
// workspace root) to its cleaned absolute spelling abs and the existing,
// symlink-resolved path real. Files must be
// markdown. It returns the HTTP status to report when the path is refused.
func (a *API) resolveOpenPath(requested string) (abs, real string, isDir bool, status int, message string) {
	abs = filepath.Clean(requested)
	if !filepath.IsAbs(requested) {
		safe, err := a.reg.defaultWS.safePath(requested)
		if err != nil {
			return "", "", false, http.StatusForbidden, "forbidden"
		}
		abs = safe
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if isMissing(err) {
			return "", "", false, http.StatusNotFound, "not found"
		}
		return "", "", false, http.StatusForbidden, "forbidden"
	}
	info, err := os.Stat(real)
	if err != nil {
		if isMissing(err) {
			return "", "", false, http.StatusNotFound, "not found"
		}
		return "", "", false, http.StatusInternalServerError, "internal error"
	}
	if info.IsDir() {
		return abs, real, true, http.StatusOK, ""
	}
	ext := strings.ToLower(filepath.Ext(real))
	if ext != ".md" && ext != ".markdown" {
		return "", "", false, http.StatusBadRequest, "only markdown files or directories can be opened"
	}
	return abs, real, false, http.StatusOK, ""
}

// HandleOpen serves POST /api/open with {"path":"..."}: an absolute path or a
// path relative to the default workspace root. A path already served by a
// workspace reuses it; any other directory or markdown file gets a new
// workspace. It answers {"url", "clients", "workspace", "created"}: url is
// what the browser should open ("/w/<id>/", plus "#<percent-encoded rel>" for
// a file) and clients counts WebSocket clients connected to that workspace. A
// file target is also broadcast to that workspace's tabs as a navigate message.
func (a *API) HandleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.exposed {
		writeJSONError(w, http.StatusForbidden, "hand-off is disabled: this server is bound to a non-loopback host")
		return
	}
	if !a.originAllowed(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	got := r.Header.Get(OpenTokenHeader)
	if a.openToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.openToken)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req struct {
		Path string `json:"path"`
	}
	if err := decoder.Decode(&req); err != nil || req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "bad request")
		return
	}

	abs, real, isDir, status, message := a.resolveOpenPath(req.Path)
	if status != http.StatusOK {
		writeJSONError(w, status, message)
		return
	}
	out, err := a.reg.open(abs, real, isDir)
	if err != nil {
		status, message := openStatus(err)
		writeJSONError(w, status, message)
		return
	}

	// An escaped fragment keeps spaces, '#' and '%' in file names intact; the
	// page must percent-decode location.hash.
	page := url.URL{Path: "/w/" + out.ws.id + "/", Fragment: out.rel}
	if out.rel != "" {
		msg, _ := json.Marshal(map[string]string{"type": "navigate", "path": out.rel})
		out.ws.hub.Broadcast(msg)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":       page.String(),
		"clients":   out.ws.hub.ClientCount(),
		"workspace": out.ws.id,
		"created":   out.created,
	})
}

// HandleWorkspaces serves GET /api/workspaces: every workspace in creation order.
func (a *API) HandleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type entry struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Root    string `json:"root"`
		Target  string `json:"target"`
		URL     string `json:"url"`
		Clients int    `json:"clients"`
		Default bool   `json:"default"`
		// Watching is false when live reload is off for the workspace
		// (directory too large, or the shared watch budget is used up).
		Watching bool `json:"watching"`
	}
	list := a.reg.snapshot()
	entries := make([]entry, 0, len(list))
	for _, ws := range list {
		entries = append(entries, entry{
			ID:      ws.id,
			Name:    ws.name(),
			Root:    ws.root,
			Target:  ws.target,
			URL:     "/w/" + ws.id + "/",
			Clients: ws.hub.ClientCount(),
			Default: a.reg.isDefault(ws),

			Watching: ws.watcher != nil,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}
