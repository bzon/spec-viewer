package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRelativeDocumentPath(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(realRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(realRoot, "docs", "x.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "link")
	symlinkErr := os.Symlink(realRoot, linkRoot)

	tests := []struct {
		name     string
		root     string
		path     string
		want     string
		wantOK   bool
		needLink bool
	}{
		{"file in root", realRoot, filepath.Join(realRoot, "a.md"), "a.md", true, false},
		{"nested file", realRoot, file, "docs/x.md", true, false},
		{"outside root", realRoot, filepath.Join(base, "other", "a.md"), "", false, false},
		{"sibling prefix", realRoot, realRoot + "-2/a.md", "", false, false},
		{"root itself", realRoot, realRoot, "", false, false},
		{"symlinked root", linkRoot, file, "docs/x.md", true, true},
		{"symlinked path, real root", realRoot, filepath.Join(linkRoot, "docs", "x.md"), "docs/x.md", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needLink && symlinkErr != nil {
				t.Skipf("symlinks unsupported: %v", symlinkErr)
			}
			got, ok := RelativeDocumentPath(tt.root, tt.path)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRelativeDocumentPathUsesSlashes(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "r")
	got, ok := RelativeDocumentPath(root, filepath.Join(root, "a", "b.md"))
	if !ok || got != "a/b.md" {
		t.Errorf("got (%q, %v)", got, ok)
	}
}
