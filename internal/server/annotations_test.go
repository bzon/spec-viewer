package server_test

import (
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotationsPersist(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.md"), "# Document")
	storePath := filepath.Join(t.TempDir(), "annotations.json")
	api := server.NewAPIWithAnnotations(root, server.NewHub(), "", storePath)

	created := time.Now().UTC().Truncate(time.Second)
	body := `{"annotations":[{"id":"highlight-1","quote":"Document","prefix":"# ","note":"Review this","created_at":"` + created.Format(time.RFC3339) + `","updated_at":"` + created.Format(time.RFC3339) + `"}]}`
	put := httptest.NewRequest(http.MethodPut, "/api/annotations?path=doc.md", strings.NewReader(body))
	putRecorder := httptest.NewRecorder()
	api.HandleAnnotations(putRecorder, put)
	if putRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", putRecorder.Code, putRecorder.Body.String())
	}

	// A new API instance should read the annotation from disk.
	api = server.NewAPIWithAnnotations(root, server.NewHub(), "", storePath)
	get := httptest.NewRequest(http.MethodGet, "/api/annotations?path=doc.md", nil)
	getRecorder := httptest.NewRecorder()
	api.HandleAnnotations(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRecorder.Code)
	}

	var annotations []server.Annotation
	if err := json.NewDecoder(getRecorder.Body).Decode(&annotations); err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 1 || annotations[0].Note != "Review this" {
		t.Fatalf("unexpected annotations: %#v", annotations)
	}
}

func TestAnnotationsRejectInvalidInput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.md"), "# Document")
	api := server.NewAPIWithAnnotations(root, server.NewHub(), "", filepath.Join(t.TempDir(), "annotations.json"))

	tests := []struct {
		name string
		url  string
		body string
		want int
	}{
		{"path traversal", "/api/annotations?path=../doc.md", "", http.StatusForbidden},
		{"non-markdown", "/api/annotations?path=doc.txt", "", http.StatusBadRequest},
		{"empty quote", "/api/annotations?path=doc.md", `{"annotations":[{"id":"one","quote":""}]}`, http.StatusBadRequest},
		{"duplicate id", "/api/annotations?path=doc.md", `{"annotations":[{"id":"one","quote":"a"},{"id":"one","quote":"b"}]}`, http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, test.url, strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			api.HandleAnnotations(recorder, req)
			if recorder.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, recorder.Code, recorder.Body.String())
			}
		})
	}
}
