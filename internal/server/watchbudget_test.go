package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bzon/spec-viewer/internal/watcher"
)

func watchingFlags(t *testing.T, api *API) []bool {
	t.Helper()
	rec := httptest.NewRecorder()
	api.HandleWorkspaces(rec, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	var list []struct {
		Watching bool `json:"watching"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	flags := make([]bool, len(list))
	for i, e := range list {
		flags[i] = e.Watching
	}
	return flags
}

// Single-file workspaces cost one watched directory each on every platform,
// which makes the budget arithmetic platform independent.
func TestWatchBudgetIsSharedAcrossWorkspaces(t *testing.T) {
	first := touchMarkdown(t, filepath.Join(tempDir(t), "a.md"))
	api := NewAPIWithAnnotations(filepath.Dir(first), NewHub(), "a.md", filepath.Join(tempDir(t), "ann.json"))
	t.Cleanup(api.Close)
	api.reg.watchBudget = watcher.NewBudget(2)
	if err := api.EnableWatching(); err != nil {
		t.Fatal(err)
	}

	second := touchMarkdown(t, filepath.Join(tempDir(t), "b.md"))
	third := touchMarkdown(t, filepath.Join(tempDir(t), "c.md"))
	out2 := mustOpen(t, api.reg, second, false)
	out3 := mustOpen(t, api.reg, third, false)

	got := watchingFlags(t, api)
	if want := []bool{true, true, false}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("watching=%v, want %v", got, want)
	}
	if used := api.reg.watchBudget.Used(); used != 2 {
		t.Fatalf("used=%d, want 2", used)
	}

	// Closing a workspace returns its share, closing twice returns it once.
	out2.ws.close()
	out2.ws.close()
	if used := api.reg.watchBudget.Used(); used != 1 {
		t.Fatalf("used after close=%d, want 1", used)
	}
	out3.ws.close()
	fourth := touchMarkdown(t, filepath.Join(tempDir(t), "d.md"))
	mustOpen(t, api.reg, fourth, false)
	if got := watchingFlags(t, api); !got[len(got)-1] {
		t.Fatalf("new workspace not watched after budget was released: %v", got)
	}
}

func TestRegistryClosesWorkspaceAddedWhileClosing(t *testing.T) {
	r, _ := newTestRegistry(t, MaxWorkspaces)
	r.closeAll()
	file := touchMarkdown(t, filepath.Join(tempDir(t), "late.md"))
	_, err := r.open(file, file, false)
	if !errors.Is(err, errRegistryClosed) {
		t.Fatalf("err=%v, want errRegistryClosed", err)
	}
	if status, _ := openStatus(err); status != http.StatusServiceUnavailable {
		t.Errorf("status %d", status)
	}
	if r.count() != 1 {
		t.Errorf("count=%d, late workspace was kept", r.count())
	}
}

func TestWorkspaceCloseIsIdempotent(t *testing.T) {
	ws := newWorkspace(tempDir(t), "", NewHub())
	ws.close()
	ws.close()
}
