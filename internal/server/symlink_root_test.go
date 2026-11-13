package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bzon/spec-viewer/internal/server"
)

// symlinkRootFixture is a real server whose workspaces sit on symlinked roots:
// linkDefault -> realDefault serves as the default workspace, linkOpened ->
// realOpened is added through /api/open.
type symlinkRootFixture struct {
	twoWorkspaces
	linkDefault, realDefault string
	linkOpened, realOpened   string
}

// newSymlinkRootFixture builds the fixture. A dangling link is only included
// when withDanglingLink is set: the kqueue watcher (macOS, BSD) refuses a
// directory holding one, which is independent of root symlinks.
func newSymlinkRootFixture(t *testing.T, withDanglingLink bool) symlinkRootFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	base := realTempDir(t)
	outside := filepath.Join(base, "outside")
	f := symlinkRootFixture{
		realDefault: filepath.Join(base, "real-default"),
		linkDefault: filepath.Join(base, "link-default"),
		realOpened:  filepath.Join(base, "real-opened"),
		linkOpened:  filepath.Join(base, "link-opened"),
	}
	for _, dir := range []string{outside, f.realDefault, f.realOpened} {
		if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{f.realDefault, f.realOpened} {
		for file, body := range map[string]string{
			"linked.md":          "# linked",
			"sub/nested.md":      "# nested",
			"sub/deep/deeper.md": "# deeper",
			"inside-target.md":   "# inside target",
		} {
			if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("# secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "hidden.md"), []byte("# hidden"), 0o644); err != nil {
			t.Fatal(err)
		}
		links := map[string]string{
			"escape.md":     filepath.Join(outside, "secret.md"), // file link leaving the root
			"alias.md":      filepath.Join(root, "inside-target.md"),
			"linked-subdir": outside, // directory link: not followed
		}
		if withDanglingLink {
			links["broken.md"] = filepath.Join(root, "nothing.md")
		}
		for name, target := range links {
			if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
				t.Skipf("symlinks unsupported: %v", err)
			}
		}
	}
	for link, real := range map[string]string{f.linkDefault: f.realDefault, f.linkOpened: f.realOpened} {
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
	}

	assets := fstest.MapFS{"index.html": {Data: []byte("<html>themes/github-dark.css</html>")}}
	srv, err := server.New(f.linkDefault, assets, "127.0.0.1", 0, "github-dark", "")
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
	f.twoWorkspaces = twoWorkspaces{srv: srv, base: srv.URL(), rootA: f.linkDefault, rootB: f.linkOpened,
		annotations: filepath.Join(configDir, "spec-viewer", "annotations.json")}
	f.idA = f.workspaces(t)[0].ID
	opened := f.open(t, f.linkOpened)
	if !opened.Created {
		t.Fatalf("opened workspace not created: %+v", opened)
	}
	f.idB = opened.Workspace
	return f
}

func (f symlinkRootFixture) listed(t *testing.T, id string) []string {
	t.Helper()
	code, body := f.get(t, "/api/files?ws="+id)
	if code != http.StatusOK {
		t.Fatalf("files: %d %s", code, body)
	}
	var entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		if e.Name != e.Path {
			t.Errorf("name %q != path %q", e.Name, e.Path)
		}
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	return paths
}

func (f symlinkRootFixture) each(t *testing.T, fn func(t *testing.T, id, link, real string)) {
	t.Helper()
	t.Run("default", func(t *testing.T) { fn(t, f.idA, f.linkDefault, f.realDefault) })
	t.Run("opened", func(t *testing.T) { fn(t, f.idB, f.linkOpened, f.realOpened) })
}

func TestSymlinkedRootListsFilesAndHidesUnreadableLinks(t *testing.T) {
	f := newSymlinkRootFixture(t, true)
	want := []string{"alias.md", "inside-target.md", "linked.md", "sub/deep/deeper.md", "sub/nested.md"}
	f.each(t, func(t *testing.T, id, link, real string) {
		got := f.listed(t, id)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("listed %v, want %v", got, want)
		}
		// Everything listed can be opened; escaping and unlisted links cannot.
		for _, p := range got {
			if code, body := f.get(t, "/api/file?ws="+id+"&path="+p); code != http.StatusOK {
				t.Errorf("listed %s returned %d %s", p, code, body)
			}
		}
		for _, p := range []string{"escape.md", "linked-subdir/hidden.md"} {
			if code, _ := f.get(t, "/api/file?ws="+id+"&path="+p); code != http.StatusForbidden {
				t.Errorf("%s: status %d, want 403", p, code)
			}
		}
	})
}

func TestSymlinkedRootAnnotationsKeepUnresolvedRoot(t *testing.T) {
	f := newSymlinkRootFixture(t, false)
	put := `{"annotations":[{"id":"n1","quote":"linked","note":"kept","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]}`
	f.each(t, func(t *testing.T, id, link, real string) {
		if code, body := f.do(t, http.MethodPut, "/api/annotations?path=linked.md&ws="+id, "application/json", put); code != http.StatusNoContent {
			t.Fatalf("put: %d %s", code, body)
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
		if _, ok := stored.Documents[filepath.Join(link, "linked.md")]; !ok {
			t.Errorf("key %q missing in %v", filepath.Join(link, "linked.md"), stored.Documents)
		}
		for _, e := range f.workspaces(t) {
			if e.ID == id && e.Root != link {
				t.Errorf("workspace root %q, want %q", e.Root, link)
			}
		}
	})
}

func TestSymlinkedRootReloadPathMatchesListing(t *testing.T) {
	f := newSymlinkRootFixture(t, false)
	f.each(t, func(t *testing.T, id, link, real string) {
		c := f.connect(t, id)
		for _, rel := range []string{"linked.md", "sub/deep/deeper.md"} {
			if !contains(f.listed(t, id), rel) {
				t.Fatalf("%s not listed", rel)
			}
			if err := os.WriteFile(filepath.Join(real, filepath.FromSlash(rel)), []byte("# changed "+rel), 0o644); err != nil {
				t.Fatal(err)
			}
			// Writes may produce several debounced events; wait for one for rel.
			deadline := time.After(5 * time.Second)
			for found := false; !found; {
				select {
				case m := <-c.msgs:
					found = m["type"] == "reload" && m["path"] == rel
				case <-deadline:
					t.Fatalf("no reload message for %s", rel)
				}
			}
		}
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
