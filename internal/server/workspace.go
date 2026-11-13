package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/bzon/spec-viewer/internal/watcher"
)

// MaxWorkspaces is the most workspaces one server keeps at a time.
const MaxWorkspaces = 32

// workspace is one served folder, or one markdown file in single-file mode,
// with its own broadcast hub and file watcher.
type workspace struct {
	id string
	// root is the absolute root as the server was given it. Annotation keys
	// are built from it, so it must not change when symlinks are resolved.
	root string
	// realRoot is root with symlinks resolved; it is what ids and the
	// open-path matching are derived from.
	realRoot string
	// target is the file name relative to root in single-file mode, else empty.
	target string
	hub    *Hub

	watcher   io.Closer // nil when no watcher is running
	lastUsed  uint64    // registry tick of the last open or API access; guarded by registry.mu
	closeOnce sync.Once
}

// workspaceID derives the stable workspace id: the first 10 hex characters of
// SHA-256 over realRoot, a NUL byte and target.
func workspaceID(realRoot, target string) string {
	sum := sha256.Sum256([]byte(realRoot + "\x00" + target))
	return hex.EncodeToString(sum[:])[:10]
}

func newWorkspace(root, target string, hub *Hub) *workspace {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		real = abs
	}
	return &workspace{
		id:       workspaceID(real, target),
		root:     abs,
		realRoot: real,
		target:   target,
		hub:      hub,
	}
}

// name is the display name: the target file's base name, else the root's.
func (ws *workspace) name() string {
	if ws.target != "" {
		return filepath.Base(ws.target)
	}
	return filepath.Base(ws.root)
}

// watchPath is the path handed to the file watcher.
func (ws *workspace) watchPath() string {
	if ws.target != "" {
		return filepath.Join(ws.root, ws.target)
	}
	return ws.root
}

// safePath resolves rel within the workspace root and refuses paths that escape it.
func (ws *workspace) safePath(rel string) (string, error) {
	abs := filepath.Clean(filepath.Join(ws.root, filepath.FromSlash(rel)))
	rootWithSep := ws.root
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	if abs != ws.root && !strings.HasPrefix(abs, rootWithSep) {
		return "", os.ErrPermission
	}
	return abs, nil
}

// resolveInside is safePath plus a symlink check: when the path exists, its
// symlink-resolved form must stay under the resolved workspace root, so a link
// inside the root cannot lead outside it. A path that does not exist passes
// (the caller reports it as missing).
func (ws *workspace) resolveInside(rel string) (string, error) {
	abs, err := ws.safePath(rel)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if isMissing(err) {
			return abs, nil
		}
		return "", os.ErrPermission
	}
	if resolved != ws.realRoot {
		if _, ok := relativeInside(ws.realRoot, resolved); !ok {
			return "", os.ErrPermission
		}
	}
	return abs, nil
}

// isMissing reports a path that does not exist, including one whose parent is
// not a directory.
func isMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// allowsDocument reports whether rel may be read as a document: any path in
// directory mode, only the target in single-file mode.
func (ws *workspace) allowsDocument(rel string) bool {
	if ws.target == "" {
		return true
	}
	clean := func(p string) string { return strings.TrimPrefix(path.Clean(filepath.ToSlash(p)), "./") }
	return clean(rel) == clean(ws.target)
}

// rootMissing reports that the workspace's root folder no longer exists.
func (ws *workspace) rootMissing() bool {
	info, err := os.Stat(ws.root)
	return err != nil && isMissing(err) || err == nil && !info.IsDir()
}

// alternateDocumentPaths lists other spellings of the document at abs (inside
// root) under which older data may be stored: the symlink-resolved root joined
// with the same relative path, and the fully resolved path. The primary
// spelling abs is never listed.
func (ws *workspace) alternateDocumentPaths(abs string) []string {
	var alts []string
	add := func(p string) {
		if p == "" || p == abs {
			return
		}
		for _, have := range alts {
			if have == p {
				return
			}
		}
		alts = append(alts, p)
	}
	if rel, ok := relativeInside(ws.root, abs); ok {
		add(filepath.Join(ws.realRoot, filepath.FromSlash(rel)))
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		add(resolved)
	}
	return alts
}

// close stops the watcher and disconnects the hub's clients. It is safe to
// call more than once.
func (ws *workspace) close() {
	ws.closeOnce.Do(func() {
		if ws.watcher != nil {
			_ = ws.watcher.Close()
		}
		ws.hub.Close()
	})
}

// registry holds the workspaces of a server. Its mutex guards only the
// in-memory list; file system work and watcher start/stop happen outside it.
type registry struct {
	// createMu serialises workspace creation so the capacity check and the
	// insert cannot race. It is never taken by lookups.
	createMu sync.Mutex

	mu        sync.Mutex
	list      []*workspace // creation order
	defaultWS *workspace
	tick      uint64
	limit     int

	// watchBudget is shared by the watchers of every workspace.
	watchBudget *watcher.Budget
	closed      bool

	// startWatcher, when set, starts the file watcher of a new workspace.
	startWatcher func(*workspace) (io.Closer, error)
}

func newRegistry(def *workspace) *registry {
	r := &registry{defaultWS: def, list: []*workspace{def}, limit: MaxWorkspaces, watchBudget: watcher.NewBudget(watcher.DefaultBudget)}
	r.touchLocked(def)
	return r
}

func (r *registry) touchLocked(ws *workspace) {
	r.tick++
	ws.lastUsed = r.tick
}

// lookup returns the workspace with the given id, or the default workspace for
// an empty id, and records the access.
func (r *registry) lookup(id string) (*workspace, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" {
		r.touchLocked(r.defaultWS)
		return r.defaultWS, true
	}
	for _, ws := range r.list {
		if ws.id == id {
			r.touchLocked(ws)
			return ws, true
		}
	}
	return nil, false
}

// snapshot returns the workspaces in creation order with their client counts.
func (r *registry) snapshot() []*workspace {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*workspace(nil), r.list...)
}

func (r *registry) isDefault(ws *workspace) bool { return ws == r.defaultWS }

func (r *registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.list)
}

// matchLocked finds the workspace that already serves real (a symlink-resolved
// path). For a file the deepest directory-mode root containing it wins, then a
// single-file workspace on exactly that file; rel is the root-relative path.
// For a directory only a directory-mode workspace with that root matches.
// The caller holds r.mu.
func (r *registry) matchLocked(real string, isDir bool) (ws *workspace, rel string) {
	if isDir {
		for _, c := range r.list {
			if c.target == "" && c.realRoot == real {
				return c, ""
			}
		}
		return nil, ""
	}
	for _, c := range r.list {
		if c.target != "" {
			continue
		}
		inner, ok := relativeInside(c.realRoot, real)
		if ok && (ws == nil || len(c.realRoot) > len(ws.realRoot)) {
			ws, rel = c, inner
		}
	}
	if ws != nil {
		return ws, rel
	}
	for _, c := range r.list {
		if c.target != "" && filepath.Join(c.realRoot, c.target) == real {
			return c, c.target
		}
	}
	return nil, ""
}

// evictLocked removes and returns the least recently used workspace that has
// no connected clients and is not the default, or nil if there is none.
func (r *registry) evictLocked() *workspace {
	idx := -1
	for i, c := range r.list {
		if c == r.defaultWS || c.hub.ClientCount() > 0 {
			continue
		}
		if idx < 0 || c.lastUsed < r.list[idx].lastUsed {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	ev := r.list[idx]
	r.list = append(r.list[:idx], r.list[idx+1:]...)
	return ev
}

var (
	errRegistryFull   = errors.New("too many workspaces are in use")
	errFilesystemRoot = errors.New("refusing to serve the filesystem root")
	errRegistryClosed = errors.New("the server is shutting down")
)

// openOutcome is the workspace an open request resolved to.
type openOutcome struct {
	ws      *workspace
	rel     string // root-relative slash path of the document; empty for a directory
	created bool
}

// open returns the workspace for real, a symlink-resolved existing path, creating
// one when none serves it. requested is the same path as the caller spelled it
// (absolute and cleaned, not symlink-resolved); a new workspace keeps its
// directory as the root, which annotation keys are built from, so a folder gets
// the same keys however it was first opened. A markdown file with no
// workspace becomes a single-file workspace on its parent directory.
func (r *registry) open(requested, real string, isDir bool) (openOutcome, error) {
	r.createMu.Lock()
	defer r.createMu.Unlock()

	r.mu.Lock()
	if ws, rel := r.matchLocked(real, isDir); ws != nil {
		r.touchLocked(ws)
		r.mu.Unlock()
		return openOutcome{ws: ws, rel: rel}, nil
	}
	r.mu.Unlock()

	root, realRoot, target := requested, real, ""
	if !isDir {
		realRoot, target = filepath.Dir(real), filepath.Base(real)
		root = filepath.Dir(requested)
		// A file that is itself a symlink is served from where it points.
		if info, err := os.Lstat(requested); err != nil || info.Mode()&os.ModeSymlink != 0 {
			root = realRoot
		}
	}
	if got, err := filepath.EvalSymlinks(root); err != nil || got != realRoot {
		root = realRoot // the spelling does not lead to the resolved folder
	}
	if realRoot == filepath.Dir(realRoot) {
		return openOutcome{}, errFilesystemRoot
	}

	var evicted *workspace
	r.mu.Lock()
	if len(r.list) >= r.limit {
		if evicted = r.evictLocked(); evicted == nil {
			r.mu.Unlock()
			return openOutcome{}, errRegistryFull
		}
	}
	r.mu.Unlock()
	if evicted != nil {
		evicted.close()
	}

	ws := newWorkspace(root, target, NewHub())
	if r.startWatcher != nil {
		w, err := r.startWatcher(ws)
		if err != nil {
			log.Printf("warning: no live reload for %s: %v", ws.watchPath(), err)
		} else {
			ws.watcher = w
		}
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		ws.close()
		return openOutcome{}, errRegistryClosed
	}
	r.list = append(r.list, ws)
	r.touchLocked(ws)
	r.mu.Unlock()
	return openOutcome{ws: ws, rel: target, created: true}, nil
}

// closeAll stops every workspace's watcher and hub.
func (r *registry) closeAll() {
	r.mu.Lock()
	r.closed = true
	all := append([]*workspace(nil), r.list...)
	r.mu.Unlock()
	for _, ws := range all {
		ws.close()
	}
}

// openStatus maps an open error to an HTTP status and message.
func openStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errRegistryFull):
		return http.StatusConflict, err.Error()
	case errors.Is(err, errFilesystemRoot):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, errRegistryClosed):
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusInternalServerError, "internal error"
}
