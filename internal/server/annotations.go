package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxAnnotations = 1000
	maxNoteLength  = 10000
	maxQuoteLength = 10000
)

// Annotation is a personal highlight anchored by its selected text and context.
type Annotation struct {
	ID        string    `json:"id"`
	Quote     string    `json:"quote"`
	Prefix    string    `json:"prefix,omitempty"`
	Suffix    string    `json:"suffix,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type annotationData struct {
	Documents map[string][]Annotation `json:"documents"`
}

type annotationStore struct {
	path string
	mu   sync.Mutex
}

func defaultAnnotationsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "spec-viewer", "annotations.json")
}

func (s *annotationStore) get(document string) ([]Annotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if err != nil {
		return nil, err
	}
	annotations := data.Documents[document]
	if annotations == nil {
		annotations = []Annotation{}
	}
	return annotations, nil
}

func (s *annotationStore) put(document string, annotations []Annotation) error {
	if err := validateAnnotations(annotations); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if err != nil {
		return err
	}
	if len(annotations) == 0 {
		delete(data.Documents, document)
	} else {
		data.Documents[document] = annotations
	}
	return s.write(data)
}

func (s *annotationStore) read() (annotationData, error) {
	data := annotationData{Documents: make(map[string][]Annotation)}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("decode annotations: %w", err)
	}
	if data.Documents == nil {
		data.Documents = make(map[string][]Annotation)
	}
	return data, nil
}

func (s *annotationStore) write(data annotationData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "annotations-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func validateAnnotations(annotations []Annotation) error {
	if len(annotations) > maxAnnotations {
		return errors.New("too many annotations")
	}
	seen := make(map[string]struct{}, len(annotations))
	for _, annotation := range annotations {
		if annotation.ID == "" || len(annotation.ID) > 128 {
			return errors.New("invalid annotation id")
		}
		if _, exists := seen[annotation.ID]; exists {
			return errors.New("duplicate annotation id")
		}
		seen[annotation.ID] = struct{}{}
		if strings.TrimSpace(annotation.Quote) == "" || len(annotation.Quote) > maxQuoteLength {
			return errors.New("invalid annotation quote")
		}
		if len(annotation.Prefix) > 200 || len(annotation.Suffix) > 200 {
			return errors.New("annotation context is too long")
		}
		if len(annotation.Note) > maxNoteLength {
			return errors.New("annotation note is too long")
		}
	}
	return nil
}

// HandleAnnotations serves GET and PUT /api/annotations?path=X.
func (a *API) HandleAnnotations(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".md" && ext != ".markdown" {
		http.Error(w, "only markdown files are supported", http.StatusBadRequest)
		return
	}
	document, err := a.safePath(rel)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(document); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		annotations, err := a.annotations.get(document)
		if err != nil {
			http.Error(w, "could not load annotations", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(annotations)
	case http.MethodPut:
		var req struct {
			Annotations []Annotation `json:"annotations"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Annotations == nil {
			req.Annotations = []Annotation{}
		}
		if err := a.annotations.put(document, req.Annotations); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
