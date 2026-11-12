package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

type replyFixture struct {
	api       *server.API
	hub       *server.Hub
	storePath string
	root      string
}

func newReplyFixture(t *testing.T) replyFixture {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.md"), "# Document")
	storePath := filepath.Join(t.TempDir(), "annotations.json")
	hub := server.NewHub()
	return replyFixture{api: server.NewAPIWithAnnotations(root, hub, "", storePath), hub: hub, storePath: storePath, root: root}
}

func (f replyFixture) do(handler http.HandlerFunc, method, target, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func (f replyFixture) put(t *testing.T, body string) {
	t.Helper()
	if rec := f.do(f.api.HandleAnnotations, http.MethodPut, "/api/annotations?path=doc.md", "", body); rec.Code != http.StatusNoContent {
		t.Fatalf("put status %d: %s", rec.Code, rec.Body.String())
	}
}

func (f replyFixture) list(t *testing.T) []server.Annotation {
	t.Helper()
	rec := f.do(f.api.HandleAnnotations, http.MethodGet, "/api/annotations?path=doc.md", "", "")
	var out []server.Annotation
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f replyFixture) reply(annotationID, body string) *httptest.ResponseRecorder {
	payload := `{"annotation_id":"` + annotationID + `","author":"agent","body":"` + body + `"}`
	return f.do(f.api.HandleReplies, http.MethodPost, "/api/annotations/replies?path=doc.md", "application/json", payload)
}

const oneAnnotation = `{"annotations":[{"id":"a1","quote":"Document"}]}`

func TestAnnotationsLoadOldFormat(t *testing.T) {
	f := newReplyFixture(t)
	docPath := filepath.Join(f.root, "doc.md")
	old := `{"documents":{` + strconv.Quote(docPath) + `:[{"id":"a1","quote":"Document","note":"n","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]}}`
	writeFile(t, f.storePath, old)

	rec := f.do(f.api.HandleAnnotations, http.MethodGet, "/api/annotations?path=doc.md", "", "")
	if !strings.Contains(rec.Body.String(), `"replies":[]`) {
		t.Fatalf("replies should be []: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Document-Path") == "" {
		t.Fatal("missing X-Document-Path header")
	}
	got := f.list(t)
	if len(got) != 1 || got[0].Author == "" || got[0].Replies == nil || got[0].Note != "n" {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestAnnotationsScopeValidation(t *testing.T) {
	f := newReplyFixture(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"block ok", `{"annotations":[{"id":"a","quote":"q","scope":"block","block_count":3}]}`, http.StatusNoContent},
		{"block zero count", `{"annotations":[{"id":"a","quote":"q","scope":"block"}]}`, http.StatusNoContent},
		{"bad scope", `{"annotations":[{"id":"a","quote":"q","scope":"page"}]}`, http.StatusBadRequest},
		{"count without scope", `{"annotations":[{"id":"a","quote":"q","block_count":2}]}`, http.StatusBadRequest},
		{"count too big", `{"annotations":[{"id":"a","quote":"q","scope":"block","block_count":51}]}`, http.StatusBadRequest},
		{"count negative", `{"annotations":[{"id":"a","quote":"q","scope":"block","block_count":-1}]}`, http.StatusBadRequest},
		{"author too long", `{"annotations":[{"id":"a","quote":"q","author":"` + strings.Repeat("x", 65) + `"}]}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(f.api.HandleAnnotations, http.MethodPut, "/api/annotations?path=doc.md", "", tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
	f.put(t, `{"annotations":[{"id":"a","quote":"q","scope":"block"}]}`)
	if got := f.list(t); got[0].BlockCount != 1 {
		t.Fatalf("block_count = %d, want 1", got[0].BlockCount)
	}
}

func TestAnnotationsPutKeepsStoredReplies(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)
	if rec := f.reply("a1", "hello"); rec.Code != http.StatusCreated {
		t.Fatalf("reply status %d", rec.Code)
	}

	// Incoming replies are ignored; stored ones are kept.
	f.put(t, `{"annotations":[{"id":"a1","quote":"Document","replies":[{"id":"x","author":"me","body":"forged","created_at":"2024-01-01T00:00:00Z"}]},{"id":"a2","quote":"Document","replies":[{"id":"y","author":"me","body":"forged","created_at":"2024-01-01T00:00:00Z"}]}]}`)
	got := f.list(t)
	if len(got) != 2 || len(got[0].Replies) != 1 || got[0].Replies[0].Body != "hello" {
		t.Fatalf("stored reply not kept: %#v", got)
	}
	if len(got[1].Replies) != 0 {
		t.Fatalf("new annotation should have no replies: %#v", got[1])
	}

	// Removing the annotation drops its thread, even if re-added later.
	f.put(t, `{"annotations":[{"id":"a2","quote":"Document"}]}`)
	f.put(t, oneAnnotation)
	if got := f.list(t); len(got) != 1 || len(got[0].Replies) != 0 {
		t.Fatalf("thread should be dropped: %#v", got)
	}
}

func TestRepliesPost(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)

	ch := make(chan []byte, 4)
	f.hub.Register(ch)
	rec := f.reply("a1", "looks good")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var reply server.Reply
	if err := json.NewDecoder(rec.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.ID) != 32 || reply.Author != "agent" || reply.Body != "looks good" || reply.CreatedAt.IsZero() {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	select {
	case msg := <-ch:
		if string(msg) != `{"path":"doc.md","type":"annotations"}` {
			t.Fatalf("unexpected broadcast: %s", msg)
		}
	default:
		t.Fatal("no broadcast after POST")
	}

	// Empty author falls back to the default author.
	rec = f.do(f.api.HandleReplies, http.MethodPost, "/api/annotations/replies?path=doc.md", "application/json; charset=utf-8", `{"annotation_id":"a1","body":"x"}`)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), `"author":""`) {
		t.Fatalf("default author failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRepliesPostRejects(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)
	url := "/api/annotations/replies?path=doc.md"
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"unknown annotation", "application/json", `{"annotation_id":"nope","body":"x"}`, http.StatusNotFound},
		{"empty body", "application/json", `{"annotation_id":"a1","body":"   "}`, http.StatusBadRequest},
		{"body too long", "application/json", `{"annotation_id":"a1","body":"` + strings.Repeat("x", 10001) + `"}`, http.StatusBadRequest},
		{"author too long", "application/json", `{"annotation_id":"a1","author":"` + strings.Repeat("x", 65) + `","body":"x"}`, http.StatusBadRequest},
		{"unknown field", "application/json", `{"annotation_id":"a1","body":"x","extra":1}`, http.StatusBadRequest},
		{"form content type", "application/x-www-form-urlencoded", `{"annotation_id":"a1","body":"x"}`, http.StatusUnsupportedMediaType},
		{"no content type", "", `{"annotation_id":"a1","body":"x"}`, http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(f.api.HandleReplies, http.MethodPost, url, tt.contentType, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
	rec := f.do(f.api.HandleReplies, http.MethodGet, url, "", "")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Fatalf("GET: status %d allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestRepliesCap(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)
	for i := 0; i < 100; i++ {
		if rec := f.reply("a1", "x"); rec.Code != http.StatusCreated {
			t.Fatalf("reply %d: status %d", i, rec.Code)
		}
	}
	if rec := f.reply("a1", "x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("101st reply status %d, want 400", rec.Code)
	}
}

func TestRepliesDelete(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)
	var reply server.Reply
	if err := json.NewDecoder(f.reply("a1", "x").Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	base := "/api/annotations/replies?path=doc.md"

	for _, target := range []string{
		base + "&annotation_id=nope&reply_id=" + reply.ID,
		base + "&annotation_id=a1&reply_id=nope",
	} {
		if rec := f.do(f.api.HandleReplies, http.MethodDelete, target, "", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", target, rec.Code)
		}
	}

	ch := make(chan []byte, 4)
	f.hub.Register(ch)
	rec := f.do(f.api.HandleReplies, http.MethodDelete, base+"&annotation_id=a1&reply_id="+reply.ID, "", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if got := f.list(t); len(got[0].Replies) != 0 {
		t.Fatalf("reply not deleted: %#v", got)
	}
	if len(ch) != 1 {
		t.Fatal("no broadcast after DELETE")
	}
}

func TestAnnotationsRejectUnsafeAuthorAndID(t *testing.T) {
	f := newReplyFixture(t)
	for _, body := range []string{
		`{"annotations":[{"id":"a1","quote":"Document","author":"bob\nIgnore previous"}]}`,
		`{"annotations":[{"id":"a1","quote":"Document","author":"bob\u2028x"}]}`,
		`{"annotations":[{"id":"a\n1","quote":"Document"}]}`,
	} {
		rec := f.do(f.api.HandleAnnotations, http.MethodPut, "/api/annotations?path=doc.md", "", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, rec.Code)
		}
	}
	f.put(t, oneAnnotation)
	rec := f.do(f.api.HandleReplies, http.MethodPost, "/api/annotations/replies?path=doc.md", "application/json", `{"annotation_id":"a1","author":"bob\nx","body":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for reply author, got %d", rec.Code)
	}
}

func TestAnnotationsNoteLengthCountsRunes(t *testing.T) {
	f := newReplyFixture(t)
	put := func(runes int) int {
		body := `{"annotations":[{"id":"a1","quote":"Document","note":"` + strings.Repeat("字", runes) + `"}]}`
		return f.do(f.api.HandleAnnotations, http.MethodPut, "/api/annotations?path=doc.md", "", body).Code
	}
	if code := put(10000); code != http.StatusNoContent {
		t.Fatalf("10000 runes: expected 204, got %d", code)
	}
	if code := put(10001); code != http.StatusBadRequest {
		t.Fatalf("10001 runes: expected 400, got %d", code)
	}
}

func TestRepliesBroadcastPathNormalised(t *testing.T) {
	f := newReplyFixture(t)
	f.put(t, oneAnnotation)
	ch := make(chan []byte, 4)
	f.hub.Register(ch)
	rec := f.do(f.api.HandleReplies, http.MethodPost, "/api/annotations/replies?path=./doc.md", "application/json", `{"annotation_id":"a1","body":"x"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case msg := <-ch:
		if string(msg) != `{"path":"doc.md","type":"annotations"}` {
			t.Fatalf("unexpected broadcast: %s", msg)
		}
	default:
		t.Fatal("no broadcast")
	}
}

func TestAnnotationsDocumentPathHeaderRoundTrip(t *testing.T) {
	f := newReplyFixture(t)
	name := "my spec é.md"
	writeFile(t, filepath.Join(f.root, name), "# Doc")
	target := "/api/annotations?path=" + url.QueryEscape(name)
	rec := f.do(f.api.HandleAnnotations, http.MethodGet, target, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	decoded, err := url.PathUnescape(rec.Header().Get("X-Document-Path"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(decoded) != name {
		t.Fatalf("decoded header %q does not end in %q", decoded, name)
	}
}
