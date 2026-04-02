package server

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"nhooyr.io/websocket"
)

// Server holds all components needed to serve the spec-viewer UI.
type Server struct {
	hub      *Hub
	api      *API
	assets   fs.FS
	listener net.Listener
	srv      *http.Server
	theme    string
}

// New creates a new Server. It binds the listener, creates the Hub and API,
// and wires up all HTTP routes.
func New(root string, assets fs.FS, host string, port int, theme string, targetFile string) (*Server, error) {
	hub := NewHub()
	api := NewAPI(root, hub, targetFile)

	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	s := &Server{
		hub:      hub,
		api:      api,
		assets:   assets,
		listener: ln,
		theme:    theme,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/file", api.HandleFile)
	mux.HandleFunc("/api/files", api.HandleFiles)
	mux.HandleFunc("/api/themes", api.HandleThemes)
	mux.HandleFunc("/api/open", api.HandleOpen)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/css/themes/", s.handleThemeCSS)
	mux.HandleFunc("/", s.handleRoot)

	s.srv = &http.Server{Handler: mux}
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

// Hub returns the underlying Hub.
func (s *Server) Hub() *Hub {
	return s.hub
}

// Start begins serving HTTP requests. It blocks until the server stops.
func (s *Server) Start() error {
	return s.srv.Serve(s.listener)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
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
	if _, statErr := os.Stat(customPath); statErr == nil {
		http.ServeFile(w, r, customPath)
		return
	}

	http.NotFound(w, r)
}

// handleRoot serves index.html with the configured theme CSS injected, or
// serves other static assets from the embedded FS.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "/index.html" {
		s.serveIndex(w, r)
		return
	}

	// Serve from embedded assets.
	rel := strings.TrimPrefix(path, "/")
	if _, err := fs.Stat(s.assets, rel); err == nil {
		http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
		return
	}

	// Fallback: serve static files (images, etc.) from root directory.
	// Only serve known safe file types to avoid leaking source code.
	if isStaticAsset(rel) {
		localPath := filepath.Join(s.api.root, filepath.FromSlash(rel))
		localPath = filepath.Clean(localPath)
		if strings.HasPrefix(localPath, s.api.root) {
			if _, err := os.Stat(localPath); err == nil {
				http.ServeFile(w, r, localPath)
				return
			}
		}
	}

	http.NotFound(w, r)
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
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ch := make(chan []byte, 16)
	s.hub.Register(ch)
	defer s.hub.Unregister(ch)

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
