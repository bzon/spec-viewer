package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// MaxWatchDirs is the maximum number of directories allowed for recursive watching.
// Exceeding this likely means the user pointed at a full codebase rather than a docs directory.
const MaxWatchDirs = 1000

// errDirTooLarge is a sentinel used to stop the walk early.
var errDirTooLarge = errors.New("directory too large")

// MaxWatchFiles caps the files under a watched directory on platforms whose
// watcher (kqueue) holds one open file descriptor per file. Exceeding it would
// exhaust the process's descriptors, so such a directory is not watched.
const MaxWatchFiles = 5000

// watchCostsPerFile reports whether the platform's watcher opens a descriptor per file.
var watchCostsPerFile = runtime.GOOS == "darwin" || strings.HasSuffix(runtime.GOOS, "bsd")

// countEntries walks root counting directories and files. It stops early once
// either count exceeds its limit and returns the counts so far. Real walk
// errors (e.g. permission denied) are returned as the third value.
func countEntries(root string, maxDirs, maxFiles int) (dirs, files int, err error) {
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs++
		} else {
			files++
		}
		if dirs > maxDirs || files > maxFiles {
			return errDirTooLarge
		}
		return nil
	})
	if err == errDirTooLarge {
		return dirs, files, nil
	}
	return dirs, files, err
}

// DefaultBudget is the default number of watched directories (and, where the
// watcher holds a descriptor per file, files) all watchers of one process may
// hold together. kqueue platforms (macOS, BSD) hold one descriptor per watched
// file and directory, and a process gets roughly 10k descriptors on macOS, so
// 4000 leaves room for sockets and reads; it is also below the long-standing
// Linux inotify default of 8192 watches per user.
const DefaultBudget = 4000

// ErrBudgetExhausted means starting a watcher would exceed the shared Budget.
var ErrBudgetExhausted = errors.New("watch budget exhausted")

// Budget is a process-wide limit on watched entries shared by several watchers.
// A nil *Budget is unlimited.
type Budget struct {
	mu    sync.Mutex
	limit int
	used  int
}

// NewBudget returns a Budget allowing limit watched entries in total.
func NewBudget(limit int) *Budget {
	return &Budget{limit: limit}
}

// Used returns the number of entries currently charged.
func (b *Budget) Used() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func (b *Budget) acquire(n int) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

func (b *Budget) release(n int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

// OnChange is called with the path of the changed markdown file.
type OnChange func(path string)

// Watcher watches a file or directory for markdown changes.
type Watcher struct {
	fsw      *fsnotify.Watcher
	onChange OnChange
	done     chan struct{}

	budget    *Budget
	cost      int
	closeOnce sync.Once
	closeErr  error
}

// New creates a Watcher for the given path (file or directory).
func New(path string, onChange OnChange) (*Watcher, error) {
	return NewWithBudget(path, onChange, nil)
}

// NewWithBudget is New with the watcher's entries charged to budget until it is
// closed. It fails with ErrBudgetExhausted when the budget cannot cover them.
func NewWithBudget(path string, onChange OnChange, budget *Budget) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Walk and watch the resolved path: filepath.Walk does not descend into a
	// root that is itself a symlink, and events then carry resolved paths.
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
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
	w.budget = budget
	w.cost = 1 // the parent directory of a watched file
	if isDir {
		maxFiles := int(^uint(0) >> 1)
		if watchCostsPerFile {
			maxFiles = MaxWatchFiles
		}
		dirCount, fileCount, countErr := countEntries(path, MaxWatchDirs, maxFiles)
		if countErr != nil {
			fsw.Close()
			return nil, fmt.Errorf("counting directories: %w", countErr)
		}
		if dirCount > MaxWatchDirs {
			fsw.Close()
			return nil, fmt.Errorf("directory too large for watching (found %d+ directories, max %d) — run \"spec-viewer <file.md>\" to view a specific file", dirCount, MaxWatchDirs)
		}
		if fileCount > maxFiles {
			fsw.Close()
			return nil, fmt.Errorf("directory too large for watching (found %d+ files, max %d) — run \"spec-viewer <file.md>\" to view a specific file", fileCount, maxFiles)
		}
		w.cost = dirCount
		if watchCostsPerFile {
			w.cost += fileCount
		}
	}
	if !budget.acquire(w.cost) {
		fsw.Close()
		return nil, fmt.Errorf("%w: %d more entries needed, %d of the shared limit already in use", ErrBudgetExhausted, w.cost, budget.Used())
	}
	if isDir {
		if err := watchRecursive(fsw, path); err != nil {
			fsw.Close()
			budget.release(w.cost)
			return nil, err
		}
	} else {
		// Watch parent directory to detect writes to the target file.
		if err := fsw.Add(filepath.Dir(path)); err != nil {
			fsw.Close()
			budget.release(w.cost)
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

// Close stops the watcher and returns its budget charge. It is safe to call
// more than once.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		w.closeErr = w.fsw.Close()
		w.budget.release(w.cost)
	})
	return w.closeErr
}
