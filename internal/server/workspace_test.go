package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func touchMarkdown(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestRegistry(t *testing.T, limit int) (*registry, string) {
	t.Helper()
	root := tempDir(t)
	r := newRegistry(newWorkspace(root, "", NewHub()))
	r.limit = limit
	return r, root
}

func mustOpen(t *testing.T, r *registry, path string, isDir bool) openOutcome {
	t.Helper()
	out, err := r.open(path, path, isDir)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return out
}

func TestWorkspaceIDIsStableAndDerivedFromRootAndTarget(t *testing.T) {
	root := tempDir(t)
	a := newWorkspace(root, "", NewHub())
	b := newWorkspace(root, "", NewHub())
	if a.id != b.id {
		t.Errorf("same path gave ids %s and %s", a.id, b.id)
	}
	sum := sha256.Sum256([]byte(root + "\x00"))
	if want := hex.EncodeToString(sum[:])[:10]; a.id != want {
		t.Errorf("id %s, want %s", a.id, want)
	}
	if file := newWorkspace(root, "a.md", NewHub()); file.id == a.id {
		t.Error("single-file workspace shares the directory's id")
	}
	if other := newWorkspace(tempDir(t), "", NewHub()); other.id == a.id {
		t.Error("different roots share an id")
	}
}

func TestWorkspaceIDUsesResolvedRoot(t *testing.T) {
	real := tempDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	viaLink := newWorkspace(link, "", NewHub())
	if want := newWorkspace(real, "", NewHub()); viaLink.id != want.id {
		t.Errorf("id via link %s, want %s", viaLink.id, want.id)
	}
	if viaLink.root != link {
		t.Errorf("root %s, want the path as given %s", viaLink.root, link)
	}
}

func TestRegistryReuseRules(t *testing.T) {
	r, root := newTestRegistry(t, MaxWorkspaces)
	def := r.defaultWS

	deep := touchMarkdown(t, filepath.Join(root, "a", "b", "c.md"))
	if out := mustOpen(t, r, deep, false); out.ws != def || out.rel != "a/b/c.md" || out.created {
		t.Errorf("file in default root: %+v", out)
	}
	if out := mustOpen(t, r, root, true); out.ws != def || out.rel != "" || out.created {
		t.Errorf("default root dir: %+v", out)
	}

	mid := mustOpen(t, r, filepath.Join(root, "a"), true)
	if !mid.created || mid.ws == def {
		t.Fatalf("subdir: %+v", mid)
	}
	innermost := mustOpen(t, r, filepath.Join(root, "a", "b"), true)
	if !innermost.created {
		t.Fatalf("inner subdir: %+v", innermost)
	}
	if out := mustOpen(t, r, deep, false); out.ws != innermost.ws || out.rel != "c.md" {
		t.Errorf("deepest root should win: %+v", out)
	}
	other := touchMarkdown(t, filepath.Join(root, "a", "d.md"))
	if out := mustOpen(t, r, other, false); out.ws != mid.ws || out.rel != "d.md" {
		t.Errorf("file in middle root: %+v", out)
	}
	if out := mustOpen(t, r, filepath.Join(root, "a"), true); out.ws != mid.ws || out.created {
		t.Errorf("dir equality: %+v", out)
	}
}

func TestRegistrySingleFileWorkspace(t *testing.T) {
	r, _ := newTestRegistry(t, MaxWorkspaces)
	dir := tempDir(t)
	file := touchMarkdown(t, filepath.Join(dir, "n.md"))
	touchMarkdown(t, filepath.Join(dir, "m.md"))

	first := mustOpen(t, r, file, false)
	if !first.created || first.ws.root != dir || first.ws.target != "n.md" || first.rel != "n.md" {
		t.Fatalf("first: %+v ws=%+v", first, first.ws)
	}
	if again := mustOpen(t, r, file, false); again.created || again.ws != first.ws {
		t.Errorf("reuse: %+v", again)
	}
	// A sibling file is not covered by the single-file workspace.
	if sib := mustOpen(t, r, filepath.Join(dir, "m.md"), false); !sib.created || sib.ws == first.ws {
		t.Errorf("sibling: %+v", sib)
	}
	// Opening the folder creates a directory workspace; it then wins for files.
	dirWS := mustOpen(t, r, dir, true)
	if !dirWS.created || dirWS.ws.target != "" {
		t.Fatalf("dir: %+v", dirWS)
	}
	if out := mustOpen(t, r, file, false); out.ws != dirWS.ws {
		t.Errorf("file should resolve to directory workspace: %+v", out)
	}
}

func TestRegistryRefusesFilesystemRoot(t *testing.T) {
	r, _ := newTestRegistry(t, MaxWorkspaces)
	root := string(filepath.Separator)
	if _, err := r.open(root, root, true); !errors.Is(err, errFilesystemRoot) {
		t.Errorf("dir: err=%v", err)
	}
	if _, err := r.open(filepath.Join(root, "x.md"), filepath.Join(root, "x.md"), false); !errors.Is(err, errFilesystemRoot) {
		t.Errorf("file at root: err=%v", err)
	}
	if r.count() != 1 {
		t.Errorf("count=%d", r.count())
	}
}

func TestRegistryEvictsLeastRecentlyUsedIdleWorkspace(t *testing.T) {
	r, _ := newTestRegistry(t, 3)
	a := mustOpen(t, r, tempDir(t), true).ws
	b := mustOpen(t, r, tempDir(t), true).ws
	if r.count() != 3 {
		t.Fatalf("count=%d", r.count())
	}
	// Access a, so b is the least recently used; the default is older still.
	if _, ok := r.lookup(a.id); !ok {
		t.Fatal("lookup a")
	}
	c := mustOpen(t, r, tempDir(t), true)
	if !c.created || r.count() != 3 {
		t.Fatalf("c=%+v count=%d", c, r.count())
	}
	if _, ok := r.lookup(b.id); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := r.lookup(a.id); !ok {
		t.Error("a should remain")
	}
	if _, ok := r.lookup(""); !ok || r.defaultWS == nil {
		t.Error("default must remain")
	}
	// The evicted hub rejects new clients, so a racing WebSocket ends at once.
	ch := make(chan []byte, 1)
	b.hub.Register(ch)
	if _, open := <-ch; open {
		t.Error("evicted hub accepted a client")
	}
}

func TestRegistryEvictionSkipsWorkspacesWithClientsAndNeverEvictsDefault(t *testing.T) {
	r, _ := newTestRegistry(t, 3)
	a := mustOpen(t, r, tempDir(t), true).ws
	b := mustOpen(t, r, tempDir(t), true).ws
	a.hub.Register(make(chan []byte, 1))

	// a is least recently used but connected, so b goes.
	mustOpen(t, r, tempDir(t), true)
	if _, ok := r.lookup(b.id); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := r.lookup(a.id); !ok {
		t.Error("a has a client and must stay")
	}

	// Connect every non-default workspace: nothing can be evicted, even though
	// the default is idle and the oldest.
	for _, ws := range r.snapshot() {
		if ws != r.defaultWS && ws.hub.ClientCount() == 0 {
			ws.hub.Register(make(chan []byte, 1))
		}
	}
	full := tempDir(t)
	_, err := r.open(full, full, true)
	if !errors.Is(err, errRegistryFull) {
		t.Fatalf("err=%v, want errRegistryFull", err)
	}
	if status, _ := openStatus(err); status != 409 {
		t.Errorf("status=%d", status)
	}
	if r.count() != 3 || r.snapshot()[0] != r.defaultWS {
		t.Error("registry changed after a refused open")
	}
}

func TestRegistryListKeepsCreationOrder(t *testing.T) {
	r, _ := newTestRegistry(t, MaxWorkspaces)
	a := mustOpen(t, r, tempDir(t), true).ws
	b := mustOpen(t, r, tempDir(t), true).ws
	list := r.snapshot()
	if len(list) != 3 || list[0] != r.defaultWS || list[1] != a || list[2] != b {
		t.Errorf("order: %v", list)
	}
}
