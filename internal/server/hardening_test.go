package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bzon/spec-viewer/internal/server"
	"nhooyr.io/websocket"
)

const hardeningToken = "hardening-token"

// startServer runs a real server on root (in single-file mode when target is
// set) with HOME pointing at an empty temp dir and returns it.
func startServer(t *testing.T, root, host, target string) *server.Server {
	t.Helper()
	if os.Getenv("HOME") == "" || !strings.HasPrefix(os.Getenv("HOME"), os.TempDir()) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	}
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>themes/github-dark.css</html>")}}
	srv, err := server.New(root, assets, host, 0, "github-dark", target)
	if err != nil {
		t.Skipf("cannot listen on %q: %v", host, err)
	}
	srv.SetHandoff(hardeningToken, "test")
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// req sends a request to the server's 127.0.0.1 address with an optional Host override.
func req(t *testing.T, srv *server.Server, method, path, host, contentType, body string, header map[string]string) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(srv.Port())+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		r.Host = host
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func handOff(t *testing.T, srv *server.Server, path string) openResp {
	t.Helper()
	resp, body := req(t, srv, http.MethodPost, "/api/open", "", "application/json", pathBody(path),
		map[string]string{server.OpenTokenHeader: hardeningToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open %s: %d %s", path, resp.StatusCode, body)
	}
	var out openResp
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
}

func TestHandleOpenRefusedWhenNetworkExposed(t *testing.T) {
	f := newOpenFixture(t, "")
	f.api.SetExposed(true)
	w := f.open(f.root)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Header().Get("Content-Type"), "json") ||
		!strings.Contains(w.Body.String(), "hand-off is disabled") {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
}

func TestExposedServerRefusesHandOffOverHTTP(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	srv := startServer(t, root, "0.0.0.0", "")
	resp, body := req(t, srv, http.MethodPost, "/api/open", "", "application/json", pathBody(root),
		map[string]string{server.OpenTokenHeader: hardeningToken})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
}

func TestHostHeaderValidation(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	srv := startServer(t, root, "127.0.0.1", "")
	port := strconv.Itoa(srv.Port())

	for _, path := range []string{"/", "/api/file?path=doc.md", "/api/workspaces", "/api/health", "/css/x.css", "/w/anything/"} {
		resp, _ := req(t, srv, http.MethodGet, path, "evil.example", "", "", nil)
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s with evil Host: status %d, want 421", path, resp.StatusCode)
		}
		resp, _ = req(t, srv, http.MethodGet, path, "evil.example:"+port, "", "", nil)
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s with evil Host and port: status %d, want 421", path, resp.StatusCode)
		}
	}

	// The WebSocket upgrade is refused before the handshake.
	resp, _ := req(t, srv, http.MethodGet, "/ws", "evil.example:"+port, "", "", map[string]string{
		"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
	})
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("websocket upgrade with evil Host: status %d, want 421", resp.StatusCode)
	}

	for _, host := range []string{"localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port, "LOCALHOST:" + port} {
		for _, path := range []string{"/api/health", "/api/file?path=doc.md", "/api/workspaces"} {
			if resp, body := req(t, srv, http.MethodGet, path, host, "", "", nil); resp.StatusCode != http.StatusOK {
				t.Errorf("%s with Host %s: status %d: %s", path, host, resp.StatusCode, body)
			}
		}
	}
}

func TestHostHeaderAllowsConfiguredHost(t *testing.T) {
	root := realTempDir(t)
	srv := startServer(t, root, "0.0.0.0", "")
	port := strconv.Itoa(srv.Port())
	if resp, _ := req(t, srv, http.MethodGet, "/api/health", "192.168.7.7:"+port, "", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("wildcard bind with IP literal Host: %d", resp.StatusCode)
	}
	if resp, _ := req(t, srv, http.MethodGet, "/api/health", "evil.example:"+port, "", "", nil); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("wildcard bind with DNS name Host: %d", resp.StatusCode)
	}
}

func TestWebSocketOrigins(t *testing.T) {
	root := realTempDir(t)
	srv := startServer(t, root, "127.0.0.1", "")
	port := strconv.Itoa(srv.Port())
	dial := func(origin string) error {
		ctx := context.Background()
		conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+port+"/ws", &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": {origin}},
		})
		if err == nil {
			conn.CloseNow()
		}
		return err
	}
	for _, origin := range []string{"http://localhost:" + port, "http://127.0.0.1:" + port, "http://[::1]:" + port} {
		if err := dial(origin); err != nil {
			t.Errorf("origin %s refused: %v", origin, err)
		}
	}
	for _, origin := range []string{"http://localhost:1", "http://[::1]:1", "http://evil.example:" + port} {
		if err := dial(origin); err == nil {
			t.Errorf("origin %s accepted", origin)
		}
	}
}

func TestAnnotationKeyIsSameHoweverFolderWasOpened(t *testing.T) {
	realDir := realTempDir(t)
	writeDoc(t, filepath.Join(realDir, "proj", "doc.md"))
	link := filepath.Join(realTempDir(t), "link")
	symlinkOrSkip(t, realDir, link)
	linkProj := filepath.Join(link, "proj")

	other := realTempDir(t)
	writeDoc(t, filepath.Join(other, "x.md"))

	// First instance serves the folder through the symlink.
	first := startServer(t, linkProj, "127.0.0.1", "")
	// A second instance receives it by hand-off.
	second := startServer(t, other, "127.0.0.1", "")
	opened := handOff(t, second, linkProj)
	if !opened.Created {
		t.Fatalf("expected a new workspace: %+v", opened)
	}

	r1, _ := req(t, first, http.MethodGet, "/api/annotations?path=doc.md", "", "", "", nil)
	r2, _ := req(t, second, http.MethodGet, "/api/annotations?ws="+opened.Workspace+"&path=doc.md", "", "", "", nil)
	k1, k2 := r1.Header.Get("X-Document-Path"), r2.Header.Get("X-Document-Path")
	if k1 == "" || k1 != k2 {
		t.Fatalf("document paths differ: %q vs %q", k1, k2)
	}
	if want := (&url.URL{Path: filepath.Join(linkProj, "doc.md")}).EscapedPath(); k1 != want {
		t.Errorf("key %q, want the unresolved path %q", k1, want)
	}

	// A comment created through one is read through the other.
	if resp, body := req(t, first, http.MethodPut, "/api/annotations?path=doc.md", "", "application/json", oneAnnotation, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	_, body := req(t, second, http.MethodGet, "/api/annotations?ws="+opened.Workspace+"&path=doc.md", "", "", "", nil)
	if !strings.Contains(body, `"a1"`) {
		t.Errorf("comment not visible through hand-off workspace: %s", body)
	}
}

func TestLegacyAnnotationsUnderResolvedSpellingAreFoundAndMigrated(t *testing.T) {
	realDir := realTempDir(t)
	writeDoc(t, filepath.Join(realDir, "doc.md"))
	link := filepath.Join(realTempDir(t), "link")
	symlinkOrSkip(t, realDir, link)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(configDir, "spec-viewer", "annotations.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"documents":{` + strconv.Quote(filepath.Join(realDir, "doc.md")) + `:[{"id":"old","quote":"x"}]}}`
	if err := os.WriteFile(storePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := startServer(t, link, "127.0.0.1", "")
	_, body := req(t, srv, http.MethodGet, "/api/annotations?path=doc.md", "", "", "", nil)
	if !strings.Contains(body, `"old"`) {
		t.Fatalf("legacy annotation not found: %s", body)
	}
	raw, _ := os.ReadFile(storePath)
	var stored struct {
		Documents map[string][]json.RawMessage `json:"documents"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Documents) != 1 || len(stored.Documents[filepath.Join(link, "doc.md")]) != 1 {
		t.Errorf("not migrated to the primary key: %s", raw)
	}
}

func TestConflictingAnnotationSpellingsAreNotMerged(t *testing.T) {
	realDir := realTempDir(t)
	writeDoc(t, filepath.Join(realDir, "doc.md"))
	link := filepath.Join(realTempDir(t), "link")
	symlinkOrSkip(t, realDir, link)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(configDir, "spec-viewer", "annotations.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	both := `{"documents":{` + strconv.Quote(filepath.Join(link, "doc.md")) + `:[{"id":"new","quote":"x"}],` +
		strconv.Quote(filepath.Join(realDir, "doc.md")) + `:[{"id":"old","quote":"x"}]}}`
	if err := os.WriteFile(storePath, []byte(both), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, link, "127.0.0.1", "")
	_, body := req(t, srv, http.MethodGet, "/api/annotations?path=doc.md", "", "", "", nil)
	if !strings.Contains(body, `"new"`) || strings.Contains(body, `"old"`) {
		t.Errorf("primary set must win alone: %s", body)
	}
	raw, _ := os.ReadFile(storePath)
	if !strings.Contains(string(raw), `"old"`) {
		t.Errorf("other spelling was dropped: %s", raw)
	}
}

func TestSingleFileWorkspaceServesOnlyItsTarget(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "target.md"))
	writeDoc(t, filepath.Join(root, "sibling.md"))
	writeDoc(t, filepath.Join(root, "sub", "deep.md"))
	for _, f := range []string{"pic.png", ".hidden.png", ".git/obj.png"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, f), "img")
	}
	other := realTempDir(t)
	writeDoc(t, filepath.Join(other, "x.md"))
	srv := startServer(t, other, "127.0.0.1", "")
	id := handOff(t, srv, filepath.Join(root, "target.md")).Workspace
	q := "?ws=" + id + "&path="

	for _, p := range []string{"sibling.md", "sub/deep.md", "./sibling.md", "sub/../sibling.md"} {
		for _, ep := range []string{"/api/file", "/api/annotations"} {
			resp, body := req(t, srv, http.MethodGet, ep+q+url.QueryEscape(p), "", "", "", nil)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
				t.Errorf("GET %s %s: %d %s", ep, p, resp.StatusCode, body)
			}
		}
		resp, _ := req(t, srv, http.MethodPut, "/api/annotations"+q+url.QueryEscape(p), "", "application/json", oneAnnotation, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("PUT %s: %d", p, resp.StatusCode)
		}
		resp, _ = req(t, srv, http.MethodPost, "/api/annotations/replies"+q+url.QueryEscape(p), "", "application/json", `{"annotation_id":"a1","body":"x"}`, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("replies %s: %d", p, resp.StatusCode)
		}
		resp, _ = req(t, srv, http.MethodPost, "/api/annotations/status"+q+url.QueryEscape(p), "", "application/json", `{"status":"read","annotation_ids":["a1"]}`, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status %s: %d", p, resp.StatusCode)
		}
	}
	for _, ep := range []string{"/api/file", "/api/annotations"} {
		if resp, body := req(t, srv, http.MethodGet, ep+q+"target.md", "", "", "", nil); resp.StatusCode != http.StatusOK {
			t.Errorf("target via %s: %d %s", ep, resp.StatusCode, body)
		}
	}

	// Static serving: images yes; markdown and dot paths never.
	base := "/w/" + id + "/"
	for path, want := range map[string]int{
		"pic.png": 200, "sibling.md": 404, "target.md": 404, "sub/deep.md": 404,
		".hidden.png": 404, ".git/obj.png": 404,
	} {
		if resp, _ := req(t, srv, http.MethodGet, base+path, "", "", "", nil); resp.StatusCode != want {
			t.Errorf("static %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestDotPathsAreNotServedInDirectoryWorkspaces(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	for _, f := range []string{".secret.png", ".cache/x.png", "ok.png"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, f), "img")
	}
	srv := startServer(t, root, "127.0.0.1", "")
	base := "/w/" + handOff(t, srv, root).Workspace + "/"
	for path, want := range map[string]int{"ok.png": 200, ".secret.png": 404, ".cache/x.png": 404} {
		if resp, _ := req(t, srv, http.MethodGet, base+path, "", "", "", nil); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestSymlinkEscapingTheRootIsRefused(t *testing.T) {
	outside := realTempDir(t)
	writeDoc(t, filepath.Join(outside, "secret.md"))
	writeFile(t, filepath.Join(outside, "secret.png"), "png")
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, filepath.Join(outside, "dir", "deep.md"))

	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	symlinkOrSkip(t, filepath.Join(outside, "secret.md"), filepath.Join(root, "leak.md"))
	symlinkOrSkip(t, filepath.Join(outside, "secret.png"), filepath.Join(root, "leak.png"))
	symlinkOrSkip(t, filepath.Join(outside, "dir"), filepath.Join(root, "linkdir"))
	// A symlink that stays inside the root is fine.
	symlinkOrSkip(t, filepath.Join(root, "doc.md"), filepath.Join(root, "alias.md"))

	srv := startServer(t, root, "127.0.0.1", "")
	for _, p := range []string{"leak.md", "linkdir/deep.md"} {
		for _, ep := range []string{"/api/file", "/api/annotations"} {
			if resp, body := req(t, srv, http.MethodGet, ep+"?path="+p, "", "", "", nil); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s: %d %s", ep, p, resp.StatusCode, body)
			}
		}
	}
	if resp, _ := req(t, srv, http.MethodGet, "/w/"+handOff(t, srv, root).Workspace+"/leak.png", "", "", "", nil); resp.StatusCode == http.StatusOK {
		t.Error("static symlink escape served")
	}
	if resp, _ := req(t, srv, http.MethodGet, "/api/file?path=alias.md", "", "", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("inside symlink refused: %d", resp.StatusCode)
	}
}

func TestActiveContentHeaders(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	for _, f := range []string{"a.svg", "b.pdf", "c.png", "d.html", "e.htm"} {
		writeFile(t, filepath.Join(root, f), "<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>")
	}
	srv := startServer(t, root, "127.0.0.1", "")
	base := "/w/" + handOff(t, srv, root).Workspace + "/"
	for f, sandbox := range map[string]bool{"a.svg": true, "b.pdf": true, "c.png": false} {
		resp, _ := req(t, srv, http.MethodGet, base+f, "", "", "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", f, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s nosniff=%q", f, got)
		}
		if got := resp.Header.Get("Content-Security-Policy") == "sandbox"; got != sandbox {
			t.Errorf("%s sandbox=%v, want %v", f, got, sandbox)
		}
	}
	for _, f := range []string{"d.html", "e.htm"} {
		if resp, _ := req(t, srv, http.MethodGet, base+f, "", "", "", nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s served: %d", f, resp.StatusCode)
		}
	}
}

func TestDeletedWorkspaceRootIsNotFound(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	other := realTempDir(t)
	writeDoc(t, filepath.Join(other, "x.md"))
	srv := startServer(t, other, "127.0.0.1", "")
	id := handOff(t, srv, root).Workspace
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"/api/file?ws=" + id + "&path=doc.md", "/api/files?ws=" + id, "/api/annotations?ws=" + id + "&path=doc.md"} {
		resp, body := req(t, srv, http.MethodGet, ep, "", "", "", nil)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no longer exists") {
			t.Errorf("%s: %d %s", ep, resp.StatusCode, body)
		}
	}
}

func TestWorkspacesListReportsWatching(t *testing.T) {
	root := realTempDir(t)
	writeDoc(t, filepath.Join(root, "doc.md"))
	api := server.NewAPIWithAnnotations(root, server.NewHub(), "", filepath.Join(t.TempDir(), "a.json"))
	rec := httptest.NewRecorder()
	api.HandleWorkspaces(rec, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if v, ok := list[0]["watching"]; !ok || v != false {
		t.Errorf("watching=%v ok=%v, want false (no watcher enabled)", v, ok)
	}
}
