package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxAnnotations = 1000
	maxNoteLength  = 10000
	maxQuoteLength = 10000

	maxAuthorLength   = 64
	maxReplyLength    = 10000
	maxRepliesPerNote = 100
	maxBlockCount     = 50

	scopeBlock = "block"
)

var (
	errAnnotationNotFound = errors.New("annotation not found")
	errReplyNotFound      = errors.New("reply not found")
	errTooManyReplies     = errors.New("too many replies")
)

// Annotation is a personal highlight anchored by its selected text and context.
type Annotation struct {
	ID     string `json:"id"`
	Quote  string `json:"quote"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	Note   string `json:"note,omitempty"`
	Author string `json:"author"`
	Scope  string `json:"scope,omitempty"`
	// BlockCount is the number of consecutive blocks covered when Scope is "block".
	BlockCount int       `json:"block_count,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Replies are server-owned: PUT ignores incoming values and keeps stored ones.
	Replies []Reply `json:"replies"`
}

// Reply is a threaded comment on an annotation, typically posted by an agent.
type Reply struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
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
	author := defaultAuthor()
	for i := range annotations {
		if annotations[i].Author == "" {
			annotations[i].Author = author
		}
		if annotations[i].Replies == nil {
			annotations[i].Replies = []Reply{}
		}
	}
	return annotations, nil
}

// defaultAuthor returns the current OS username, or "user" if unavailable.
func defaultAuthor() string {
	if u, err := user.Current(); err == nil {
		name := strings.Map(func(r rune) rune {
			if hasUnsafeChar(string(r)) {
				return -1
			}
			return r
		}, u.Username)
		if runes := []rune(name); len(runes) > maxAuthorLength {
			name = string(runes[:maxAuthorLength])
		}
		if name != "" {
			return name
		}
	}
	return "user"
}

// hasUnsafeChar reports control characters and line/paragraph separators,
// which could break out of a single-line field in an agent prompt.
func hasUnsafeChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

func newReplyID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *annotationStore) addReply(document, annotationID string, reply Reply) (Reply, error) {
	id, err := newReplyID()
	if err != nil {
		return Reply{}, err
	}
	reply.ID = id
	reply.CreatedAt = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if err != nil {
		return Reply{}, err
	}
	annotations := data.Documents[document]
	for i := range annotations {
		if annotations[i].ID != annotationID {
			continue
		}
		if len(annotations[i].Replies) >= maxRepliesPerNote {
			return Reply{}, errTooManyReplies
		}
		annotations[i].Replies = append(annotations[i].Replies, reply)
		return reply, s.write(data)
	}
	return Reply{}, errAnnotationNotFound
}

func (s *annotationStore) deleteReply(document, annotationID, replyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if err != nil {
		return err
	}
	annotations := data.Documents[document]
	for i := range annotations {
		if annotations[i].ID != annotationID {
			continue
		}
		for j, reply := range annotations[i].Replies {
			if reply.ID == replyID {
				annotations[i].Replies = append(annotations[i].Replies[:j], annotations[i].Replies[j+1:]...)
				return s.write(data)
			}
		}
		return errReplyNotFound
	}
	return errAnnotationNotFound
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
	// Replies are owned by the server: keep stored threads, drop incoming ones.
	storedReplies := make(map[string][]Reply)
	for _, stored := range data.Documents[document] {
		storedReplies[stored.ID] = stored.Replies
	}
	author := defaultAuthor()
	for i := range annotations {
		annotations[i].Replies = storedReplies[annotations[i].ID]
		if annotations[i].Replies == nil {
			annotations[i].Replies = []Reply{}
		}
		if annotations[i].Author == "" {
			annotations[i].Author = author
		}
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
	for i := range annotations {
		annotation := &annotations[i]
		if annotation.ID == "" || len(annotation.ID) > 128 || hasUnsafeChar(annotation.ID) {
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
		if utf8.RuneCountInString(annotation.Note) > maxNoteLength {
			return errors.New("annotation note is too long")
		}
		if utf8.RuneCountInString(annotation.Author) > maxAuthorLength {
			return errors.New("annotation author is too long")
		}
		if hasUnsafeChar(annotation.Author) {
			return errors.New("invalid annotation author")
		}
		switch annotation.Scope {
		case "":
			if annotation.BlockCount != 0 {
				return errors.New("block_count requires block scope")
			}
		case scopeBlock:
			if annotation.BlockCount == 0 {
				annotation.BlockCount = 1
			}
			if annotation.BlockCount < 1 || annotation.BlockCount > maxBlockCount {
				return errors.New("invalid block_count")
			}
		default:
			return errors.New("invalid annotation scope")
		}
	}
	return nil
}

// resolveDocument validates the path query parameter shared by the annotation
// endpoints and returns the absolute document path. On failure it writes the
// error response and returns false.
func (a *API) resolveDocument(w http.ResponseWriter, r *http.Request) (string, bool) {
	rel := r.URL.Query().Get("path")
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".md" && ext != ".markdown" {
		http.Error(w, "only markdown files are supported", http.StatusBadRequest)
		return "", false
	}
	document, err := a.safePath(rel)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	if _, err := os.Stat(document); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return "", false
	}
	return document, true
}

// HandleAnnotations serves GET and PUT /api/annotations?path=X.
func (a *API) HandleAnnotations(w http.ResponseWriter, r *http.Request) {
	document, ok := a.resolveDocument(w, r)
	if !ok {
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
		// The absolute path is percent-encoded (slashes kept) so non-ASCII
		// characters yield a valid header value; clients must percent-decode it.
		w.Header().Set("X-Document-Path", (&url.URL{Path: document}).EscapedPath())
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

// HandleReplies serves POST and DELETE /api/annotations/replies?path=X.
func (a *API) HandleReplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	document, ok := a.resolveDocument(w, r)
	if !ok {
		return
	}

	if r.Method == http.MethodDelete {
		query := r.URL.Query()
		err := a.annotations.deleteReply(document, query.Get("annotation_id"), query.Get("reply_id"))
		if !a.finishReplyChange(w, document, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		AnnotationID string `json:"annotation_id"`
		Author       string `json:"author"`
		Body         string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" || utf8.RuneCountInString(body) > maxReplyLength {
		http.Error(w, "invalid reply body", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(req.Author) > maxAuthorLength {
		http.Error(w, "reply author is too long", http.StatusBadRequest)
		return
	}
	if hasUnsafeChar(req.Author) {
		http.Error(w, "invalid reply author", http.StatusBadRequest)
		return
	}
	author := req.Author
	if author == "" {
		author = defaultAuthor()
	}

	reply, err := a.annotations.addReply(document, req.AnnotationID, Reply{Author: author, Body: body})
	if !a.finishReplyChange(w, document, err) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(reply)
}

// finishReplyChange maps a store error to a response. On success it broadcasts
// an annotations message for the document and returns true. The path matches
// the browser's current-file path (root-relative, slash-separated).
func (a *API) finishReplyChange(w http.ResponseWriter, document string, err error) bool {
	switch {
	case err == nil:
		rel, _ := RelativeDocumentPath(a.root, document)
		msg, _ := json.Marshal(map[string]string{"type": "annotations", "path": rel})
		a.hub.Broadcast(msg)
		return true
	case errors.Is(err, errAnnotationNotFound), errors.Is(err, errReplyNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errTooManyReplies):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "could not save reply", http.StatusInternalServerError)
	}
	return false
}
