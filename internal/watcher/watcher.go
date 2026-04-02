package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// OnChange is called with the path of the changed markdown file.
type OnChange func(path string)

// Watcher watches a file or directory for markdown changes.
type Watcher struct {
	fsw      *fsnotify.Watcher
	onChange OnChange
	done     chan struct{}
}

// New creates a Watcher for the given path (file or directory).
func New(path string, onChange OnChange) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		fsw.Close()
		return nil, err
	}

	w := &Watcher{
		fsw:      fsw,
		onChange: onChange,
		done:     make(chan struct{}),
	}

	isDir := info.IsDir()
	if isDir {
		if err := watchRecursive(fsw, path); err != nil {
			fsw.Close()
			return nil, err
		}
	} else {
		// Watch parent directory to detect writes to the target file.
		if err := fsw.Add(filepath.Dir(path)); err != nil {
			fsw.Close()
			return nil, err
		}
	}

	go w.loop(path, isDir)
	return w, nil
}

// watchRecursive adds all subdirectories of root to the watcher.
func watchRecursive(fsw *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return fsw.Add(p)
		}
		return nil
	})
}

// loop is the event loop that processes fsnotify events.
func (w *Watcher) loop(target string, isDir bool) {
	debounce := make(map[string]*time.Timer)
	var mu sync.Mutex

	fire := func(path string) {
		mu.Lock()
		defer mu.Unlock()
		if t, ok := debounce[path]; ok {
			t.Stop()
		}
		debounce[path] = time.AfterFunc(200*time.Millisecond, func() {
			w.onChange(path)
		})
	}

	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			switch {
			case event.Has(fsnotify.Create):
				// In directory mode, auto-add new subdirectories.
				if isDir {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = w.fsw.Add(event.Name)
					}
				}
				if isMarkdown(event.Name) {
					if isDir || event.Name == target {
						fire(event.Name)
					}
				}
			case event.Has(fsnotify.Write) || event.Has(fsnotify.Remove):
				if isMarkdown(event.Name) {
					if isDir || event.Name == target {
						fire(event.Name)
					}
				}
			}
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// isMarkdown returns true for .md and .markdown files.
func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	close(w.done)
	return w.fsw.Close()
}
