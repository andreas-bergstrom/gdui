package watch

import (
	"os"
	"os/exec"
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
//
// In linked git worktrees the per-worktree HEAD log lives outside repoRoot
// (under `<main>/.git/worktrees/<name>/logs/HEAD`), so we resolve its path
// via `git rev-parse --git-path logs/HEAD`, watch its parent directory, and
// allow that exact file through `shouldIgnore`. In main worktrees the same
// resolution lands at `<repoRoot>/.git/logs/HEAD`.
func Start(repoRoot string, debounce time.Duration, onChange func()) (stop func()) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}
	}

	addRecursive(w, repoRoot)
	headLogPath := resolveHeadLogPath(repoRoot)
	if headLogPath != "" {
		_ = w.Add(filepath.Dir(headLogPath))
	}

	done := make(chan struct{})
	go run(w, repoRoot, headLogPath, debounce, onChange, done)

	return func() {
		close(done)
		_ = w.Close()
	}
}

// resolveHeadLogPath asks git for the absolute path to logs/HEAD for the
// worktree at repoRoot. `git rev-parse --git-path logs/HEAD` returns a
// relative path for main worktrees and an absolute path for linked ones;
// we normalize to absolute relative to repoRoot. Returns "" on error.
func resolveHeadLogPath(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-path", "logs/HEAD").Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	return filepath.Clean(p)
}

func run(w *fsnotify.Watcher, repoRoot, headLogPath string, debounce time.Duration, onChange func(), done chan struct{}) {
	var timer *time.Timer
	fire := func() {
		onChange()
	}
	for {
		select {
		case <-done:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if shouldIgnore(repoRoot, ev.Name, headLogPath) {
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
		// A subdirectory with its own `.git` (dir for a nested repo, file
		// for a linked worktree or submodule) is a separate repo with its
		// own watcher — don't recurse, or every save inside it would also
		// refresh this section.
		if path != root {
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				return filepath.SkipDir
			}
		}
		_ = w.Add(path)
		return nil
	})
}

func shouldIgnore(repoRoot, path, headLogPath string) bool {
	// The per-worktree HEAD log is the only path we permit outside repoRoot —
	// in linked worktrees it lives under <main>/.git/worktrees/<name>/logs/HEAD.
	cleanPath := filepath.Clean(path)
	if headLogPath != "" && cleanPath == headLogPath {
		return false
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return true
	}
	if rel == "." {
		return true
	}
	// Outside repoRoot — ignore. The HEAD-log exception above already
	// admitted the only legitimate outside-the-tree event.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
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
		strings.HasPrefix(base, ".gdui-drop-"),
		strings.HasSuffix(base, "~"),
		strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swo"),
		strings.HasSuffix(base, ".swx"):
		return true
	}
	return false
}
