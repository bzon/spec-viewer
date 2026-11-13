package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCountEntriesStopsPastEitherLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.md", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dirs, files, err := countEntries(root, 10, 100)
	if err != nil || dirs != 1 || files != 5 {
		t.Errorf("dirs=%d files=%d err=%v", dirs, files, err)
	}
	if _, files, _ := countEntries(root, 10, 2); files != 3 {
		t.Errorf("walk should stop at the first file past the limit, counted %d", files)
	}
}
