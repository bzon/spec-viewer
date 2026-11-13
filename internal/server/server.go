package server

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

// Server holds all components needed to serve the spec-viewer UI.
type Server struct {
	api      *API
	assets   fs.FS
	listener net.Listener
	srv      *http.Server
	theme    string
	hosts    hostPolicy
}

// New creates a new Server. It binds the listener, creates the Hub and API,
// and wires up all HTTP routes.
func New(root string, assets fs.FS, host string, port int, theme string, targetFile string) (*Server, error) {
	api := NewAPI(root, NewHub(), targetFile)

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	api.SetListenPort(ln.Addr().(*net.TCPAddr).Port)
	api.SetExposed(!IsLoopbackHost(host))

	if err := api.EnableWatching(); err != nil {
		log.Printf("warning: could not start file watcher: %v", err)
	}

	s := &Server{
		api:      api,
		assets:   assets,
		listener: ln,
		theme:    theme,
		hosts:    newHostPolicy(host),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/file", api.HandleFile)
	mux.HandleFunc("/api/files", api.HandleFiles)
	mux.HandleFunc("/api/themes", api.HandleThemes)
	mux.HandleFunc("/api/annotations", api.HandleAnnotations)
	mux.HandleFunc("/api/annotations/replies", api.HandleReplies)
	mux.HandleFunc("/api/annotations/status", api.HandleAnnotationStatus)
	mux.HandleFunc("/api/open", api.HandleOpen)
	mux.HandleFunc("/api/workspaces", api.HandleWorkspaces)
	mux.HandleFunc("/api/health", api.HandleHealth)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/css/themes/", s.handleThemeCSS)
	mux.HandleFunc("/w/", s.handleWorkspace)
	mux.HandleFunc("/", s.handleRoot)

	s.srv = &http.Server{
		Handler:           s.hosts.middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Port returns the port the server is listening on.
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// URL returns the base URL for the server.
func (s *Server) URL() string {
	return fmt.Sprintf("http://localhost:%d", s.Port())
}

// SetHandoff sets the secret /api/open requires and the version /api/health reports.
func (s *Server) SetHandoff(token, version string) {
	s.api.SetHandoff(token, version)
}

// DefaultWorkspaceURL returns the full URL of the workspace the server started with.
func (s *Server) DefaultWorkspaceURL() string {
	return s.URL() + "/w/" + s.api.reg.defaultWS.id + "/"
}

// Hub returns the default workspace's Hub.
func (s *Server) Hub() *Hub {
	return s.api.reg.defaultWS.hub
}

// Start begins serving HTTP requests. It blocks until the server stops.
func (s *Server) Start() error {
	return s.srv.Serve(s.listener)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.api.Close()
	return s.srv.Shutdown(ctx)
}

// handleThemeCSS serves CSS theme files. Embedded assets are tried first;
// on miss, ~/.config/spec-viewer/themes/ is checked.
func (s *Server) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")

	// Try embedded assets first.
	if _, err := fs.Stat(s.assets, rel); err == nil {
		http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
		return
	}

	// Fall back to ~/.config/spec-viewer/themes/<basename>
	base := filepath.Base(r.URL.Path)
	customPath := filepath.Join(os.Getenv("HOME"), ".config", "spec-viewer", "themes", base)
	if _, statErr := os.Stat(customPath); statErr == nil { // #nosec G703 -- path built from filepath.Base, no traversal possible
		http.ServeFile(w, r, customPath)
		return
	}

	http.NotFound(w, r)
}

// handleRoot redirects the bare page URL to the default workspace and serves
// the app's embedded assets (css, js, fonts) from their global paths.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "/index.html" {
		http.Redirect(w, r, "/w/"+s.api.reg.defaultWS.id+"/", http.StatusFound)
		return
	}

	rel := strings.TrimPrefix(path, "/")
	if _, err := fs.Stat(s.assets, rel); err == nil {
		http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// handleWorkspace serves /w/<id>/: index.html at the workspace root and, below
// it, static files (images etc.) from that workspace's root only.
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	id, sub, hasSlash := strings.Cut(strings.TrimPrefix(r.URL.Path, "/w/"), "/")
	ws, ok := s.api.reg.lookup(id)
	if id == "" || !ok {
		http.Error(w, "unknown workspace", http.StatusNotFound)
		return
	}
	if !hasSlash {
		target := "/w/" + id + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if sub == "" || sub == "index.html" {
		s.serveIndex(w, r)
		return
	}

	// Only serve known safe file types to avoid leaking source code. Markdown
	// and other text are not on the list, so a single-file workspace cannot
	// expose its folder's documents here. Dotfiles and dot-directories are
	// never served.
	if isStaticAsset(sub) && !hasDotSegment(sub) {
		if localPath, err := ws.resolveInside(sub); err == nil {
			if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
				setActiveContentHeaders(w, localPath)
				http.ServeFile(w, r, localPath)
				return
			}
		}
	}
	http.NotFound(w, r)
}

// hasDotSegment reports a slash path with a segment starting with a dot.
func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != "" {
			return true
		}
	}
	return false
}

// setActiveContentHeaders stops a served asset from acting in the viewer's
// origin: nosniff keeps the browser to the declared type, and a sandbox CSP
// disables script in an SVG or PDF opened directly (an <img> never runs
// script anyway). HTML is not an allowed asset type.
func setActiveContentHeaders(w http.ResponseWriter, path string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg", ".pdf":
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
}

// isStaticAsset returns true for file extensions safe to serve from the root directory.
func isStaticAsset(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico",
		".pdf", ".mp4", ".webm":
		return true
	}
	return false
}

// serveIndex reads index.html from embedded assets, injects the theme CSS
// link, and writes the result.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	const indexPath = "index.html"
	data, err := fs.ReadFile(s.assets, indexPath)
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}

	// Replace the default theme reference with the configured theme.
	const defaultTheme = "themes/github-dark.css"
	configuredTheme := "themes/" + s.theme + ".css"
	content := strings.ReplaceAll(string(data), defaultTheme, configuredTheme)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

// handleWebSocket upgrades the connection and registers it with the Hub.
// It runs a write loop forwarding hub messages to the client and a read loop
// to keep the connection alive.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.api.lookupWorkspace(w, r)
	if !ok {
		return
	}
	// Besides an Origin equal to the (validated) Host, accept this server's own
	// loopback origins on its listening port: the same rule /api/open uses.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: originPatterns(s.Port()),
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ch := make(chan []byte, 16)
	ws.hub.Register(ch)
	defer ws.hub.Unregister(ch)

	ctx := conn.CloseRead(r.Context())

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case msg, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}
