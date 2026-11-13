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

const openTestToken = "test-token"

type openFixture struct {
	root string // symlink-resolved root
	api  *server.API
	hub  *server.Hub
	ch   chan []byte
}

func newOpenFixture(t *testing.T, targetFile string) openFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"docs/a.md", "sibling.md", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("# x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hub := server.NewHub()
	ch := make(chan []byte, 4)
	hub.Register(ch)
	api := server.NewAPI(root, hub, targetFile)
	api.SetHandoff(openTestToken, "test")
	api.SetListenPort(4567)
	return openFixture{root: root, api: api, hub: hub, ch: ch}
}

type openReq struct {
	method      string
	body        string
	contentType string
	token       string
	origin      string
}

func (f openFixture) do(r openReq) *httptest.ResponseRecorder {
	if r.method == "" {
		r.method = http.MethodPost
	}
	req := httptest.NewRequest(r.method, "/api/open", strings.NewReader(r.body))
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if r.token != "" {
		req.Header.Set(server.OpenTokenHeader, r.token)
	}
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	w := httptest.NewRecorder()
	f.api.HandleOpen(w, req)
	return w
}

func pathBody(p string) string {
	b, _ := json.Marshal(map[string]string{"path": p})
	return string(b)
}

func (f openFixture) open(p string) *httptest.ResponseRecorder {
	return f.do(openReq{body: pathBody(p), contentType: "application/json", token: openTestToken})
}

type openResp struct {
	URL       string `json:"url"`
	Clients   int    `json:"clients"`
	Workspace string `json:"workspace"`
	Created   bool   `json:"created"`
}

func decodeOpen(t *testing.T, w *httptest.ResponseRecorder) openResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp openResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return resp
}

// defaultID returns the id of the fixture's default workspace.
func (f openFixture) defaultID(t *testing.T) string {
	t.Helper()
	w := httptest.NewRecorder()
	f.api.HandleWorkspaces(w, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	var list []struct {
		ID      string `json:"id"`
		Default bool   `json:"default"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Default {
			return e.ID
		}
	}
	t.Fatal("no default workspace")
	return ""
}

func expectNavigate(t *testing.T, ch chan []byte, path string) {
	t.Helper()
	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"navigate"`)) || !bytes.Contains(msg, []byte(`"`+path+`"`)) {
			t.Fatalf("unexpected message %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("no navigate message")
	}
}

func expectNoMessage(t *testing.T, ch chan []byte) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected message %s", msg)
	default:
	}
}

func writeDoc(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHandleOpenAbsoluteInRoot(t *testing.T) {
	f := newOpenFixture(t, "")
	resp := decodeOpen(t, f.open(filepath.Join(f.root, "docs", "a.md")))
	if resp.URL != "/w/"+f.defaultID(t)+"/#docs/a.md" || resp.Clients != 1 || resp.Created || resp.Workspace != f.defaultID(t) {
		t.Errorf("resp=%+v", resp)
	}
	expectNavigate(t, f.ch, "docs/a.md")
}

func TestHandleOpenSymlinkedRoot(t *testing.T) {
	real := realTempDir(t)
	writeDoc(t, filepath.Join(real, "a.md"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	hub := server.NewHub()
	ch := make(chan []byte, 1)
	hub.Register(ch)
	// Serve through the symlink, open through the real path (the /tmp vs /private/tmp case).
	api := server.NewAPI(link, hub, "")
	api.SetHandoff(openTestToken, "test")
	f := openFixture{root: real, api: api, hub: hub, ch: ch}

	resp := decodeOpen(t, f.open(filepath.Join(real, "a.md")))
	if resp.Created || resp.Workspace != f.defaultID(t) || !strings.HasSuffix(resp.URL, "/#a.md") {
		t.Errorf("resp=%+v", resp)
	}
	// Opening through the link reuses the same workspace.
	if resp := decodeOpen(t, f.open(filepath.Join(link, "a.md"))); resp.Created {
		t.Errorf("link path created a workspace: %+v", resp)
	}
}

func TestHandleOpenPrivateTmpAlias(t *testing.T) {
	if _, err := os.Stat("/private/tmp"); err != nil {
		t.Skip("no /private/tmp on this system")
	}
	dir, err := os.MkdirTemp("/tmp", "svopen-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	writeDoc(t, filepath.Join(dir, "a.md"))
	api := server.NewAPI(dir, server.NewHub(), "")
	api.SetHandoff(openTestToken, "test")
	f := openFixture{root: dir, api: api, hub: server.NewHub()}
	resp := decodeOpen(t, f.open("/private"+filepath.Join(dir, "a.md")))
	if resp.Created || !strings.HasSuffix(resp.URL, "/#a.md") {
		t.Errorf("resp=%+v", resp)
	}
}

func TestHandleOpenRootRelative(t *testing.T) {
	f := newOpenFixture(t, "")
	resp := decodeOpen(t, f.open("docs/a.md"))
	if resp.Created || !strings.HasSuffix(resp.URL, "/#docs/a.md") {
		t.Errorf("resp=%+v", resp)
	}
	expectNavigate(t, f.ch, "docs/a.md")
	if w := f.open("../escape.md"); w.Code != http.StatusForbidden {
		t.Errorf("escape: status %d, want 403", w.Code)
	}
}

func TestHandleOpenRootDirectory(t *testing.T) {
	f := newOpenFixture(t, "")
	resp := decodeOpen(t, f.open(f.root))
	if resp.URL != "/w/"+f.defaultID(t)+"/" || resp.Created {
		t.Errorf("resp=%+v", resp)
	}
	expectNoMessage(t, f.ch)
}

func TestHandleOpenSubdirectoryCreatesWorkspaceAndWinsAsDeepest(t *testing.T) {
	f := newOpenFixture(t, "")
	sub := decodeOpen(t, f.open(filepath.Join(f.root, "docs")))
	if !sub.Created || sub.Workspace == f.defaultID(t) || sub.URL != "/w/"+sub.Workspace+"/" {
		t.Fatalf("sub=%+v", sub)
	}
	// A file under both roots now resolves to the deepest one.
	file := decodeOpen(t, f.open(filepath.Join(f.root, "docs", "a.md")))
	if file.Created || file.Workspace != sub.Workspace || file.URL != "/w/"+sub.Workspace+"/#a.md" {
		t.Errorf("file=%+v", file)
	}
	expectNoMessage(t, f.ch) // the default workspace's tabs are not disturbed
	// A file outside the subdirectory still belongs to the parent.
	if r := decodeOpen(t, f.open(filepath.Join(f.root, "sibling.md"))); r.Workspace != f.defaultID(t) {
		t.Errorf("sibling went to %s", r.Workspace)
	}
}

func TestHandleOpenOutsideCreatesWorkspaces(t *testing.T) {
	f := newOpenFixture(t, "")
	other := realTempDir(t)
	file := writeDoc(t, filepath.Join(other, "y.md"))

	first := decodeOpen(t, f.open(file))
	if !first.Created || first.Workspace == f.defaultID(t) || first.URL != "/w/"+first.Workspace+"/#y.md" || first.Clients != 0 {
		t.Fatalf("first=%+v", first)
	}
	again := decodeOpen(t, f.open(file))
	if again.Created || again.Workspace != first.Workspace {
		t.Errorf("again=%+v", again)
	}
	// A directory is not covered by a single-file workspace on the same folder.
	dir := decodeOpen(t, f.open(other))
	if !dir.Created || dir.Workspace == first.Workspace || dir.URL != "/w/"+dir.Workspace+"/" {
		t.Errorf("dir=%+v", dir)
	}
	// The file now belongs to the directory workspace; the single-file one is only the fallback.
	if r := decodeOpen(t, f.open(file)); r.Workspace != dir.Workspace {
		t.Errorf("file after dir opened: %+v", r)
	}
	expectNoMessage(t, f.ch)
}

func TestHandleOpenRefusals(t *testing.T) {
	f := newOpenFixture(t, "")
	tests := []struct {
		name, path string
		want       int
	}{
		{"non-markdown file", filepath.Join(f.root, "notes.txt"), http.StatusBadRequest},
		{"missing", filepath.Join(f.root, "nope.md"), http.StatusNotFound},
		{"missing relative", "nope.md", http.StatusNotFound},
		{"relative escape", "../escape.md", http.StatusForbidden},
		{"filesystem root", string(filepath.Separator), http.StatusForbidden},
	}
	for _, tt := range tests {
		w := f.open(tt.path)
		if w.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, w.Code, tt.want)
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["error"] == "" {
			t.Errorf("%s: want JSON error body, got %q", tt.name, w.Body.String())
		}
	}
	expectNoMessage(t, f.ch)
}

func TestHandleOpenSingleFileDefault(t *testing.T) {
	f := newOpenFixture(t, "sibling.md")
	own := decodeOpen(t, f.open(filepath.Join(f.root, "sibling.md")))
	if own.Created || own.Workspace != f.defaultID(t) || !strings.HasSuffix(own.URL, "/#sibling.md") {
		t.Errorf("own=%+v", own)
	}
	other := decodeOpen(t, f.open(filepath.Join(f.root, "docs", "a.md")))
	if !other.Created || other.Workspace == f.defaultID(t) {
		t.Errorf("other file: %+v", other)
	}
	dir := decodeOpen(t, f.open(f.root))
	if !dir.Created || dir.Workspace == f.defaultID(t) || dir.URL != "/w/"+dir.Workspace+"/" {
		t.Errorf("dir: %+v", dir)
	}
}

func TestHandleOpenFragmentIsPercentEncoded(t *testing.T) {
	f := newOpenFixture(t, "")
	writeDoc(t, filepath.Join(f.root, "my docs", "a #1 100%.md"))
	resp := decodeOpen(t, f.open(filepath.Join(f.root, "my docs", "a #1 100%.md")))
	want := "/w/" + f.defaultID(t) + "/#my%20docs/a%20%231%20100%25.md"
	if resp.URL != want {
		t.Errorf("url=%q want %q", resp.URL, want)
	}
	expectNavigate(t, f.ch, "my docs/a #1 100%.md")
}

func TestHandleOpenToken(t *testing.T) {
	f := newOpenFixture(t, "")
	body := pathBody("docs/a.md")
	for name, token := range map[string]string{"missing": "", "wrong": "nope"} {
		w := f.do(openReq{body: body, contentType: "application/json", token: token})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401", name, w.Code)
		}
	}
	noToken := server.NewAPI(f.root, f.hub, "")
	req := httptest.NewRequest(http.MethodPost, "/api/open", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(server.OpenTokenHeader, "")
	w := httptest.NewRecorder()
	noToken.HandleOpen(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unconfigured token: status %d, want 401", w.Code)
	}
}

func TestHandleOpenOrigin(t *testing.T) {
	f := newOpenFixture(t, "")
	body := pathBody("docs/a.md")
	for _, origin := range []string{"https://evil.example", "http://localhost:9999", "http://localhost.evil.com:4567", "https://localhost:4567", "null"} {
		w := f.do(openReq{body: body, contentType: "application/json", token: openTestToken, origin: origin})
		if w.Code != http.StatusForbidden {
			t.Errorf("origin %s: status %d, want 403", origin, w.Code)
		}
	}
	for _, origin := range []string{"http://localhost:4567", "http://127.0.0.1:4567", "http://[::1]:4567"} {
		w := f.do(openReq{body: body, contentType: "application/json", token: openTestToken, origin: origin})
		if w.Code != http.StatusOK {
			t.Errorf("origin %s: status %d, want 200", origin, w.Code)
		}
	}
}

func TestHandleOpenContentTypeAndMethod(t *testing.T) {
	f := newOpenFixture(t, "")
	w := f.do(openReq{body: pathBody("docs/a.md"), contentType: "text/plain", token: openTestToken})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: status %d, want 415", w.Code)
	}
	w = f.do(openReq{method: http.MethodGet, token: openTestToken})
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Errorf("GET: status %d Allow %q", w.Code, w.Header().Get("Allow"))
	}
}

func TestHandleOpenRejectsUnknownFields(t *testing.T) {
	f := newOpenFixture(t, "")
	w := f.do(openReq{body: `{"path":"docs/a.md","extra":1}`, contentType: "application/json", token: openTestToken})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestHandleOpenClientCount(t *testing.T) {
	f := newOpenFixture(t, "")
	f.hub.Register(make(chan []byte, 4))
	if resp := decodeOpen(t, f.open("docs/a.md")); resp.Clients != 2 {
		t.Errorf("clients=%d, want 2", resp.Clients)
	}
	// Clients are counted per workspace: a new workspace has none.
	outside := writeDoc(t, filepath.Join(realTempDir(t), "z.md"))
	if resp := decodeOpen(t, f.open(outside)); resp.Clients != 0 {
		t.Errorf("new workspace clients=%d, want 0", resp.Clients)
	}
}

func TestHandleHealth(t *testing.T) {
	f := newOpenFixture(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	f.api.HandleHealth(w, req)
	var resp struct {
		App     string `json:"app"`
		Version string `json:"version"`
		PID     int    `json:"pid"`
		Count   int    `json:"workspaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || resp.App != "spec-viewer" || resp.Version != "test" || resp.PID != os.Getpid() || resp.Count != 1 {
		t.Errorf("status %d resp %+v", w.Code, resp)
	}
}
