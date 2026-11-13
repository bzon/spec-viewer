package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bzon/spec-viewer/internal/server"
	"nhooyr.io/websocket"
)

const wsTestToken = "ws-test-token"

// twoWorkspaces runs a real server on root A and registers root B as a second
// workspace through /api/open. Both roots hold doc.md and img.png with
// different contents.
type twoWorkspaces struct {
	srv          *server.Server
	base         string
	rootA, rootB string
	idA, idB     string
	annotations  string
}

func newTwoWorkspaces(t *testing.T) twoWorkspaces {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	rootA, rootB := realTempDir(t), realTempDir(t)
	for name, root := range map[string]string{"A": rootA, "B": rootB} {
		for file, body := range map[string]string{"doc.md": "# doc " + name, "img.png": "png-" + name} {
			if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A file next to root A that traversal must not reach.
	if err := os.WriteFile(filepath.Join(filepath.Dir(rootA), "secret.png"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<html>themes/github-dark.css</html>")},
		"css/base.css":  {Data: []byte("body{}")},
		"js/app.js":     {Data: []byte("//")},
		"css/themes/x":  {Data: []byte("")},
		"unrelated.txt": {Data: []byte("")},
	}
	srv, err := server.New(rootA, assets, "127.0.0.1", 0, "github-dark", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.SetHandoff(wsTestToken, "test")
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	f := twoWorkspaces{srv: srv, base: srv.URL(), rootA: rootA, rootB: rootB,
		annotations: filepath.Join(configDir, "spec-viewer", "annotations.json")}
	f.idA = f.workspaces(t)[0].ID
	opened := f.open(t, rootB)
	if !opened.Created {
		t.Fatalf("B not created: %+v", opened)
	}
	f.idB = opened.Workspace
	return f
}

type workspaceEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Root    string `json:"root"`
	Target  string `json:"target"`
	URL     string `json:"url"`
	Clients int    `json:"clients"`
	Default bool   `json:"default"`
}

func (f twoWorkspaces) workspaces(t *testing.T) []workspaceEntry {
	t.Helper()
	_, body := f.get(t, "/api/workspaces")
	var list []workspaceEntry
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return list
}

func (f twoWorkspaces) open(t *testing.T, path string) openResp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.base+"/api/open", strings.NewReader(pathBody(path)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(server.OpenTokenHeader, wsTestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open %s: status %d: %s", path, resp.StatusCode, data)
	}
	var out openResp
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f twoWorkspaces) do(t *testing.T, method, path, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (f twoWorkspaces) get(t *testing.T, path string) (int, string) {
	t.Helper()
	return f.do(t, http.MethodGet, path, "", "")
}

func TestRootRedirectsToDefaultWorkspace(t *testing.T) {
	f := newTwoWorkspaces(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(f.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/w/"+f.idA+"/" {
		t.Errorf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, err := client.Get(f.base + "/w/" + f.idB); err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/w/"+f.idB+"/" {
		t.Errorf("no-slash form: %v %+v", err, resp)
	}
}

func TestWorkspacePageAndGlobalAssets(t *testing.T) {
	f := newTwoWorkspaces(t)
	for _, id := range []string{f.idA, f.idB} {
		code, body := f.get(t, "/w/"+id+"/")
		if code != http.StatusOK || !strings.Contains(body, "<html>") {
			t.Errorf("/w/%s/: %d %q", id, code, body)
		}
	}
	if code, _ := f.get(t, "/w/0000000000/"); code != http.StatusNotFound {
		t.Errorf("unknown workspace page: %d", code)
	}
	if code, _ := f.get(t, "/w/0000000000"); code != http.StatusNotFound {
		t.Errorf("unknown workspace no slash: %d", code)
	}
	for _, p := range []string{"/css/base.css", "/js/app.js"} {
		if code, _ := f.get(t, p); code != http.StatusOK {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestWorkspaceStaticFilesComeFromThatWorkspaceOnly(t *testing.T) {
	f := newTwoWorkspaces(t)
	if err := os.WriteFile(filepath.Join(f.rootB, "only-b.png"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := f.get(t, "/w/"+f.idA+"/img.png"); code != 200 || body != "png-A" {
		t.Errorf("A: %d %q", code, body)
	}
	if code, body := f.get(t, "/w/"+f.idB+"/img.png"); code != 200 || body != "png-B" {
		t.Errorf("B: %d %q", code, body)
	}
	if code, _ := f.get(t, "/w/"+f.idA+"/only-b.png"); code != http.StatusNotFound {
		t.Errorf("A served B's file: %d", code)
	}
	// Only the safe extensions are served.
	if code, _ := f.get(t, "/w/"+f.idA+"/doc.md"); code != http.StatusNotFound {
		t.Errorf("markdown served as static file: %d", code)
	}
}

func TestWorkspacePathTraversalRefused(t *testing.T) {
	f := newTwoWorkspaces(t)
	for _, p := range []string{"/w/" + f.idA + "/../secret.png", "/w/" + f.idA + "/..%2fsecret.png", "/w/" + f.idA + "/%2e%2e/secret.png"} {
		if code, body := f.get(t, p); code == http.StatusOK || strings.Contains(body, "secret") {
			t.Errorf("%s: status %d body %q", p, code, body)
		}
	}
}

func TestScopedAPIsReturnThatWorkspacesData(t *testing.T) {
	f := newTwoWorkspaces(t)
	for ws, want := range map[string]string{f.idA: "# doc A", f.idB: "# doc B", "": "# doc A"} {
		q := "&ws=" + ws
		if ws == "" {
			q = ""
		}
		if code, body := f.get(t, "/api/file?path=doc.md"+q); code != 200 || body != want {
			t.Errorf("ws=%q file: %d %q", ws, code, body)
		}
		code, body := f.get(t, "/api/files?"+strings.TrimPrefix(q, "&"))
		if code != 200 || !strings.Contains(body, `"doc.md"`) {
			t.Errorf("ws=%q files: %d %q", ws, code, body)
		}
	}
	if err := os.WriteFile(filepath.Join(f.rootB, "extra.md"), []byte("# e"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, body := f.get(t, "/api/files?ws="+f.idB); !strings.Contains(body, "extra.md") {
		t.Errorf("B files: %s", body)
	}
	if _, body := f.get(t, "/api/files?ws="+f.idA); strings.Contains(body, "extra.md") {
		t.Errorf("A files leak B's: %s", body)
	}
	if _, body := f.get(t, "/api/files"); strings.Contains(body, "extra.md") {
		t.Errorf("default files leak B's: %s", body)
	}
}

func TestUnknownWorkspaceIsNotFoundForEveryScopedAPI(t *testing.T) {
	f := newTwoWorkspaces(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/file?path=doc.md&ws=nope"},
		{"GET", "/api/files?ws=nope"},
		{"GET", "/api/annotations?path=doc.md&ws=nope"},
		{"POST", "/api/annotations/replies?path=doc.md&ws=nope"},
		{"POST", "/api/annotations/status?path=doc.md&ws=nope"},
	} {
		code, body := f.do(t, c.method, c.path, "application/json", "{}")
		if code != http.StatusNotFound || strings.TrimSpace(body) != `{"error":"unknown workspace"}` {
			t.Errorf("%s %s: %d %q", c.method, c.path, code, body)
		}
	}
	_, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.base, "http")+"/ws?ws=nope", nil)
	if err == nil {
		t.Error("websocket to unknown workspace connected")
	}
}

func TestAnnotationsAreKeyedByWorkspaceDocumentPath(t *testing.T) {
	f := newTwoWorkspaces(t)
	put := `{"annotations":[{"id":"n1","quote":"doc","note":"b note","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]}`
	if code, body := f.do(t, http.MethodPut, "/api/annotations?path=doc.md&ws="+f.idB, "application/json", put); code != http.StatusNoContent {
		t.Fatalf("put: %d %s", code, body)
	}
	if _, body := f.get(t, "/api/annotations?path=doc.md&ws="+f.idB); !strings.Contains(body, "b note") {
		t.Errorf("B annotations: %s", body)
	}
	for _, q := range []string{"&ws=" + f.idA, ""} {
		if _, body := f.get(t, "/api/annotations?path=doc.md"+q); strings.Contains(body, "b note") {
			t.Errorf("annotation leaked to %q: %s", q, body)
		}
	}
	raw, err := os.ReadFile(f.annotations)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Documents map[string][]server.Annotation `json:"documents"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Documents[filepath.Join(f.rootB, "doc.md")]; len(stored.Documents) != 1 || !ok {
		t.Errorf("stored keys: %v", stored.Documents)
	}
}

// wsClient reads messages from one workspace's WebSocket.
type wsClient struct {
	msgs chan map[string]string
}

func (f twoWorkspaces) connect(t *testing.T, id string) *wsClient {
	t.Helper()
	target := "ws" + strings.TrimPrefix(f.base, "http") + "/ws"
	if id != "" {
		target += "?ws=" + url.QueryEscape(id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); conn.CloseNow() })
	c := &wsClient{msgs: make(chan map[string]string, 16)}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]string
			if json.Unmarshal(data, &m) == nil {
				c.msgs <- m
			}
		}
	}()
	// The server registers the client after the handshake; wait until it is counted.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range f.workspaces(t) {
			if (id == "" && e.Default || e.ID == id) && e.Clients > 0 {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("websocket client never registered")
	return nil
}

func (c *wsClient) expect(t *testing.T, typ, path string) {
	t.Helper()
	select {
	case m := <-c.msgs:
		if m["type"] != typ || m["path"] != path {
			t.Fatalf("got %v, want type %s path %q", m, typ, path)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s message", typ)
	}
}

func (c *wsClient) expectNone(t *testing.T) {
	t.Helper()
	select {
	case m := <-c.msgs:
		t.Fatalf("unexpected message %v", m)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestWebSocketsReceiveOnlyTheirWorkspacesMessages(t *testing.T) {
	f := newTwoWorkspaces(t)
	a, b := f.connect(t, f.idA), f.connect(t, f.idB)
	noWS := f.connect(t, "") // no ws parameter means the default workspace

	// File change in A (reload, root-relative path).
	if err := os.MkdirAll(filepath.Join(f.rootA, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(f.rootA, "sub", "n.md"), []byte("# n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.expect(t, "reload", "sub/n.md")
	noWS.expect(t, "reload", "sub/n.md")
	b.expectNone(t)

	// Annotation change in B.
	reply := `{"annotation_id":"n1","body":"hi"}`
	put := `{"annotations":[{"id":"n1","quote":"doc","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]}`
	f.do(t, http.MethodPut, "/api/annotations?path=doc.md&ws="+f.idB, "application/json", put)
	if code, body := f.do(t, http.MethodPost, "/api/annotations/replies?path=doc.md&ws="+f.idB, "application/json", reply); code != 201 {
		t.Fatalf("reply: %d %s", code, body)
	}
	b.expect(t, "annotations", "doc.md")
	a.expectNone(t)
	noWS.expectNone(t)

	// Navigate in B through /api/open.
	if resp := f.open(t, filepath.Join(f.rootB, "doc.md")); resp.Workspace != f.idB || resp.Clients != 1 || resp.Created {
		t.Errorf("open: %+v", resp)
	}
	b.expect(t, "navigate", "doc.md")
	a.expectNone(t)
}

func TestWorkspacesListingAndHealth(t *testing.T) {
	f := newTwoWorkspaces(t)
	file := filepath.Join(realTempDir(t), "solo.md")
	if err := os.WriteFile(file, []byte("# s"), 0o644); err != nil {
		t.Fatal(err)
	}
	solo := f.open(t, file)

	list := f.workspaces(t)
	if len(list) != 3 {
		t.Fatalf("list: %+v", list)
	}
	want := []struct {
		id, name, target string
		def              bool
	}{
		{f.idA, filepath.Base(f.rootA), "", true},
		{f.idB, filepath.Base(f.rootB), "", false},
		{solo.Workspace, "solo.md", "solo.md", false},
	}
	for i, w := range want {
		e := list[i]
		if e.ID != w.id || e.Name != w.name || e.Target != w.target || e.Default != w.def || e.URL != "/w/"+w.id+"/" {
			t.Errorf("entry %d: %+v, want %+v", i, e, w)
		}
	}
	if list[0].Root != f.rootA || list[1].Root != f.rootB {
		t.Errorf("roots: %+v", list)
	}

	b := f.connect(t, f.idB)
	_ = b
	for _, e := range f.workspaces(t) {
		if (e.ID == f.idB) != (e.Clients == 1) {
			t.Errorf("clients for %s = %d", e.ID, e.Clients)
		}
	}

	_, body := f.get(t, "/api/health")
	var health struct {
		Workspaces int `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(body), &health); err != nil || health.Workspaces != 3 {
		t.Errorf("health %q err %v", body, err)
	}
}

func TestIDsSurviveRestart(t *testing.T) {
	f := newTwoWorkspaces(t)
	again, err := server.New(f.rootA, fstest.MapFS{"index.html": {Data: []byte("x")}}, "127.0.0.1", 0, "github-dark", "")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Shutdown(context.Background())
	if want := "/w/" + f.idA + "/"; !strings.HasSuffix(again.DefaultWorkspaceURL(), want) {
		t.Errorf("url %s, want suffix %s", again.DefaultWorkspaceURL(), want)
	}
}
