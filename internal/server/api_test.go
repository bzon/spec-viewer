package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzon/spec-viewer/internal/server"
)

func TestHandleFileValid(t *testing.T) {
	dir := t.TempDir()
	content := "# Hello World"
	f := filepath.Join(dir, "test.md")
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hub := server.NewHub()
	api := server.NewAPI(dir, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/file?path=test.md", nil)
	w := httptest.NewRecorder()
	api.HandleFile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != content {
		t.Fatalf("expected %q, got %q", content, got)
	}
}

func TestHandleFileTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	hub := server.NewHub()
	api := server.NewAPI(dir, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/file?path=../../etc/passwd", nil)
	w := httptest.NewRecorder()
	api.HandleFile(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleFiles(t *testing.T) {
	dir := t.TempDir()
	files := []struct {
		name string
	}{
		{"a.md"},
		{"b.markdown"},
		{"c.txt"},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	hub := server.NewHub()
	api := server.NewAPI(dir, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	api.HandleFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []struct {
		Name    string    `json:"name"`
		Path    string    `json:"path"`
		ModTime time.Time `json:"mod_time"`
	}
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestHandleThemes(t *testing.T) {
	dir := t.TempDir()
	hub := server.NewHub()
	api := server.NewAPI(dir, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/themes", nil)
	w := httptest.NewRecorder()
	api.HandleThemes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var themes []string
	if err := json.NewDecoder(w.Body).Decode(&themes); err != nil {
		t.Fatal(err)
	}
	if len(themes) < 5 {
		t.Fatalf("expected at least 5 themes, got %d", len(themes))
	}
}

func TestHandleOpen(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(f, []byte("# Doc"), 0644); err != nil {
		t.Fatal(err)
	}

	hub := server.NewHub()
	ch := make(chan []byte, 1)
	hub.Register(ch)

	api := server.NewAPI(dir, hub)

	body := `{"path":"doc.md"}`
	req := httptest.NewRequest(http.MethodPost, "/api/open", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.HandleOpen(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["url"] == "" {
		t.Fatal("expected non-empty url")
	}

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte("navigate")) {
			t.Fatalf("expected navigate message, got %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("hub did not receive navigate message")
	}
}

func TestHandleOpenNonMd(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(f, []byte("text"), 0644); err != nil {
		t.Fatal(err)
	}

	hub := server.NewHub()
	api := server.NewAPI(dir, hub)

	body := `{"path":"doc.txt"}`
	req := httptest.NewRequest(http.MethodPost, "/api/open", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.HandleOpen(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
