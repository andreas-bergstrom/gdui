package watch

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Start watches repoRoot recursively (excluding .git) and invokes onChange
// after a quiet period of `debounce`. Returns a stop function.
//
// Errors during setup are logged-by-omission: if we can't watch, the UI
// degrades gracefully (manual `r` still works).
func Start(repoRoot string, debounce time.Duration, onChange func()) (stop func()) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}
	}

	addRecursive(w, repoRoot)

	done := make(chan struct{})
	go run(w, repoRoot, debounce, onChange, done)

	return func() {
		close(done)
		_ = w.Close()
	}
}

func run(w *fsnotify.Watcher, repoRoot string, debounce time.Duration, onChange func(), done chan struct{}) {
	var timer *time.Timer
	fire := func() {
		onChange()
	}
	for {
		select {
		case <-done:
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if shouldIgnore(repoRoot, ev.Name) {
				continue
			}
			// Newly created directories: start watching them too.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					addRecursive(w, ev.Name)
				}
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
		case <-timerC(timer):
			timer = nil
			fire()
		case <-w.Errors:
			// drop
		}
	}
}

func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func addRecursive(w *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == ".git" || base == "node_modules" || base == "vendor" {
			return filepath.SkipDir
		}
		_ = w.Add(path)
		return nil
	})
}

func shouldIgnore(repoRoot, path string) bool {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return true
	}
	if rel == "." {
		return true
	}
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	switch first {
	case ".git", "node_modules", "vendor":
		return true
	}
	// Editor swap/lock noise.
	base := filepath.Base(path)
	switch {
	case strings.HasPrefix(base, ".#"),
		strings.HasSuffix(base, "~"),
		strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swx"):
		return true
	}
	return false
}
