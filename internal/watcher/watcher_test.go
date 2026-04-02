package watcher_test

import (
	"os"
	"path/filepath"
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
