package watcher_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bzon/spec-viewer/internal/watcher"
)

func TestWatchFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.md")

	if err := os.WriteFile(file, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var once sync.Once

	w, err := watcher.New(file, func(path string) {
		once.Do(func() { close(done) })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(file, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback not fired within 500ms")
	}
}

func TestNewDirectoryTooLarge(t *testing.T) {
	dir := t.TempDir()

	// Create MaxWatchDirs + 1 subdirectories to exceed the limit.
	for i := 0; i <= watcher.MaxWatchDirs; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("sub%d", i))
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := watcher.New(dir, func(path string) {})
	if err == nil {
		t.Fatal("expected error for directory with too many subdirs, got nil")
	}
	if !strings.Contains(err.Error(), "directory too large") {
		t.Fatalf("expected 'directory too large' in error, got: %v", err)
	}
}

func TestNewDirectoryUnderLimit(t *testing.T) {
	dir := t.TempDir()

	// Create a few subdirectories — well under the limit.
	for i := 0; i < 5; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("sub%d", i))
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Add a markdown file so the watcher has something to watch.
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := watcher.New(dir, func(path string) {})
	if err != nil {
		t.Fatalf("expected no error for small directory, got: %v", err)
	}
	w.Close()
}

func TestWatchDirectory(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.md")

	if err := os.WriteFile(existing, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var once sync.Once

	w, err := watcher.New(dir, func(path string) {
		once.Do(func() { close(done) })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	time.Sleep(50 * time.Millisecond)

	newFile := filepath.Join(dir, "new.md")
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback not fired within 500ms after creating new .md file")
	}
}
