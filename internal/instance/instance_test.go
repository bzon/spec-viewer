package instance_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/bzon/spec-viewer/internal/instance"
	"github.com/bzon/spec-viewer/internal/server"
)

func lockPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "instance.json")
}

func TestWriteAndReadLock(t *testing.T) {
	path := lockPath(t)
	info := instance.Info{Port: 8080, PID: 12345, Token: "abc"}
	if err := instance.WriteLock(path, info); err != nil {
		t.Fatalf("WriteLock error: %v", err)
	}
	got, err := instance.ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock error: %v", err)
	}
	if got != info {
		t.Errorf("got %+v, want %+v", got, info)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("lock mode %v, want 0600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestReadLockMissing(t *testing.T) {
	if _, err := instance.ReadLock("/nonexistent/path/instance.json"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestReadLockOldFormatHasNoToken(t *testing.T) {
	path := lockPath(t)
	if err := os.WriteFile(path, []byte(`{"port":1234,"pid":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := instance.ReadLock(path)
	if err != nil || info.Token != "" || info.Port != 1234 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if _, err := instance.OpenInExisting(info, "/x/a.md"); !errors.Is(err, instance.ErrNoToken) {
		t.Errorf("OpenInExisting err = %v, want ErrNoToken", err)
	}
}

func TestNewToken(t *testing.T) {
	a, err := instance.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := instance.NewToken()
	if len(a) != 64 || a == b {
		t.Errorf("token %q (len %d) not 64 hex chars / not random", a, len(a))
	}
}

func TestReleaseOnlyOwnLock(t *testing.T) {
	path := lockPath(t)
	if err := instance.WriteLock(path, instance.Info{Port: 1, PID: 111}); err != nil {
		t.Fatal(err)
	}
	instance.Release(path, 222)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("lock owned by another pid was removed")
	}
	instance.Release(path, 111)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("own lock was not removed")
	}
}

func TestAcquireLockFresh(t *testing.T) {
	path := lockPath(t)
	self := instance.Info{Port: 1, PID: 10, Token: "t"}
	owned, _, err := instance.AcquireLock(path, self, func(instance.Info) bool { return false })
	if err != nil || !owned {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	if got, _ := instance.ReadLock(path); got != self {
		t.Errorf("lock %+v", got)
	}
}

func TestAcquireLockKeepsHealthyHolder(t *testing.T) {
	path := lockPath(t)
	holder := instance.Info{Port: 5, PID: 50, Token: "holder"}
	if err := instance.WriteLock(path, holder); err != nil {
		t.Fatal(err)
	}
	owned, got, err := instance.AcquireLock(path, instance.Info{Port: 6, PID: 60, Token: "me"}, func(instance.Info) bool { return true })
	if err != nil || owned || got != holder {
		t.Fatalf("owned=%v holder=%+v err=%v", owned, got, err)
	}
	if onDisk, _ := instance.ReadLock(path); onDisk != holder {
		t.Errorf("lock was overwritten: %+v", onDisk)
	}
	// A foreign process's cleanup must not remove it either.
	instance.Release(path, 60)
	if _, err := os.Stat(path); err != nil {
		t.Error("foreign lock removed on cleanup")
	}
}

func TestAcquireLockReplacesStaleOrCorrupt(t *testing.T) {
	for name, content := range map[string]string{
		"stale":   `{"port":5,"pid":50,"token":"old"}`,
		"old":     `{"port":5,"pid":50}`,
		"corrupt": `{"port":`,
	} {
		t.Run(name, func(t *testing.T) {
			path := lockPath(t)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			self := instance.Info{Port: 6, PID: 60, Token: "me"}
			owned, _, err := instance.AcquireLock(path, self, func(instance.Info) bool { return false })
			if err != nil || !owned {
				t.Fatalf("owned=%v err=%v", owned, err)
			}
			if got, _ := instance.ReadLock(path); got != self {
				t.Errorf("lock %+v", got)
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Errorf("leftover files: %v", entries)
			}
		})
	}
}

func TestAcquireLockConcurrentSingleOwner(t *testing.T) {
	path := lockPath(t)
	const n = 16
	var owners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Any recorded holder counts as healthy: the winner must stay the winner.
			owned, _, err := instance.AcquireLock(path, instance.Info{Port: 1000 + i, PID: 100 + i, Token: "t"}, func(instance.Info) bool { return true })
			if err != nil {
				t.Errorf("AcquireLock: %v", err)
			}
			if owned {
				owners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if owners.Load() != 1 {
		t.Fatalf("%d owners, want 1", owners.Load())
	}
}

func startServer(t *testing.T, root, targetFile, token string) (*server.Server, instance.Info) {
	t.Helper()
	srv, err := server.New(root, fstest.MapFS{"index.html": {Data: []byte("x")}}, "127.0.0.1", 0, "github-dark", targetFile)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetHandoff(token, "test")
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })
	return srv, instance.Info{Port: srv.Port(), PID: os.Getpid(), Token: token}
}

func TestOpenInExistingAgainstRealServer(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	doc := filepath.Join(root, "a.md")
	if err := os.WriteFile(doc, []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, info := startServer(t, root, "", "secret")
	ch := make(chan []byte, 1)
	srv.Hub().Register(ch)

	res, err := instance.OpenInExisting(info, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.URL, "/w/") || !strings.HasSuffix(res.URL, "/#a.md") || res.Clients != 1 || res.Created || res.Workspace == "" {
		t.Errorf("result %+v", res)
	}
	select {
	case msg := <-ch:
		if !strings.Contains(string(msg), "navigate") {
			t.Errorf("message %s", msg)
		}
	default:
		t.Error("no navigate broadcast")
	}

	if _, err := instance.OpenInExisting(instance.Info{Port: info.Port, Token: "wrong"}, doc); err == nil || errors.As(err, new(*instance.RejectedError)) {
		t.Errorf("wrong token err = %v, want a non-rejection failure", err)
	}
	other := filepath.Join(t.TempDir(), "y.md")
	if err := os.WriteFile(other, []byte("# y"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = instance.OpenInExisting(info, other)
	if err != nil || !res.Created {
		t.Errorf("outside file: result %+v err %v, want a new workspace", res, err)
	}
	var rejected *instance.RejectedError
	if _, err := instance.OpenInExisting(info, filepath.Join(root, "missing.md")); !errors.As(err, &rejected) || rejected.Status != 404 {
		t.Errorf("missing file err = %v, want RejectedError 404", err)
	}
}

func TestHealthy(t *testing.T) {
	root := t.TempDir()
	_, info := startServer(t, root, "", "secret")
	if !instance.Healthy(info) {
		t.Error("real server should be healthy")
	}
	mismatch := info
	mismatch.PID++
	if instance.Healthy(mismatch) {
		t.Error("pid mismatch must not be healthy")
	}

	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"app":"other","pid":` + "1" + `}`))
	}))
	defer foreign.Close()
	if instance.Healthy(instance.Info{Port: portOf(t, foreign.URL), PID: 1}) {
		t.Error("foreign service must not be healthy")
	}
	if instance.Healthy(instance.Info{Port: 1, PID: os.Getpid()}) {
		t.Error("closed port must not be healthy")
	}
}

func portOf(t *testing.T, url string) int {
	t.Helper()
	i := strings.LastIndex(url, ":")
	var p int
	for _, c := range url[i+1:] {
		p = p*10 + int(c-'0')
	}
	return p
}

func TestTryHandoff(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	doc := filepath.Join(root, "a.md")
	if err := os.WriteFile(doc, []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, info := startServer(t, root, "", "secret")
	path := lockPath(t)

	if h := instance.TryHandoff(path, doc); h.Done || h.Holder {
		t.Errorf("no lock: %+v", h)
	}
	if err := instance.WriteLock(path, info); err != nil {
		t.Fatal(err)
	}
	h := instance.TryHandoff(path, doc)
	if !h.Done || !strings.HasSuffix(h.Result.URL, "/#a.md") {
		t.Errorf("handoff %+v", h)
	}

	stale := info
	stale.PID++
	if err := instance.WriteLock(path, stale); err != nil {
		t.Fatal(err)
	}
	if h := instance.TryHandoff(path, doc); h.Done || h.Holder {
		t.Errorf("pid mismatch treated as alive: %+v", h)
	}

	old := instance.Info{Port: info.Port, PID: info.PID}
	if err := instance.WriteLock(path, old); err != nil {
		t.Fatal(err)
	}
	if h := instance.TryHandoff(path, doc); h.Done || !errors.Is(h.Err, instance.ErrNoToken) {
		t.Errorf("tokenless lock: %+v", h)
	}
}
