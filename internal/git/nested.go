package git

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultNestedMaxDepth caps how deep we recurse looking for nested repos.
// A defensive limit — discovery skips traversing into nested .git dirs and
// the configured ignore set, so in practice depth stays small even on large
// trees, but capping defends against pathological layouts.
const DefaultNestedMaxDepth = 8

// nestedSkipDirs is the set of directory names we never descend into during
// nested-repo discovery. Mirrors the watcher's ignore list in
// internal/watch/watch.go so a section's tree and its watcher agree on what
// belongs to the repo.
var nestedSkipDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
}

// DiscoverNestedRepos walks `root` and returns a Worktree record for each
// nested git repository found directly under it. A "nested git repository"
// is any directory `dir` (other than `root` itself) that contains a `.git`
// entry — either a directory (independent repo or `git init`'d sub-dir) or
// a regular file (submodule gitlink pointing into the parent's
// `.git/modules/<name>`).
//
// The walk:
//   - does NOT follow symlinks (filepath.WalkDir behavior),
//   - skips entries named `.git`, `node_modules`, `vendor`,
//   - prunes the subtree under any discovered nested repo (callers must
//     recurse explicitly via DiscoverNestedReposRecursive to find
//     repos-inside-repos),
//   - caps walk depth at maxDepth directories below `root` (a non-positive
//     value falls back to DefaultNestedMaxDepth).
//
// Returned slice is sorted by absolute Root path (deterministic).
func DiscoverNestedRepos(root string, maxDepth int) ([]Worktree, error) {
	if maxDepth <= 0 {
		maxDepth = DefaultNestedMaxDepth
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, nil
	}

	var out []Worktree
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission denied or transient errors — skip silently.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if _, skip := nestedSkipDirs[name]; skip {
			return filepath.SkipDir
		}
		// Depth check: how many path separators are added relative to root.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if depth > maxDepth {
			return filepath.SkipDir
		}
		// Detect a nested repo: `.git` exists as file or directory.
		gitPath := filepath.Join(path, ".git")
		if _, statErr := os.Lstat(gitPath); statErr == nil {
			wt, wtErr := worktreeForNestedRoot(path)
			if wtErr == nil {
				out = append(out, wt)
			}
			// Whether we successfully built a record or not, don't descend
			// into a nested repo — its contents belong to it, not the parent.
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out, nil
}

// DiscoverNestedReposRecursive performs BFS nested-repo discovery starting
// from each input root. After discovering nested repos under a root, it
// also descends into each newly found nested repo and repeats — so callers
// get the full transitive set in one call.
//
// A visited set keyed by absolute Root prevents the same directory being
// returned twice if the same root is reachable through multiple parents
// (would only happen with symlinks, which WalkDir doesn't follow, but the
// guard is cheap insurance).
//
// `roots` is treated as the discovery seed — those roots are NOT included
// in the return value, only the nested repos found beneath them.
func DiscoverNestedReposRecursive(roots []Worktree, maxDepth int) []Worktree {
	if maxDepth <= 0 {
		maxDepth = DefaultNestedMaxDepth
	}
	visited := map[string]bool{}
	for _, r := range roots {
		visited[filepath.Clean(r.Root)] = true
	}
	var out []Worktree
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, r.Root)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		found, err := DiscoverNestedRepos(current, maxDepth)
		if err != nil {
			continue
		}
		for _, wt := range found {
			key := filepath.Clean(wt.Root)
			if visited[key] {
				continue
			}
			visited[key] = true
			out = append(out, wt)
			queue = append(queue, wt.Root)
		}
	}
	return out
}

// worktreeForNestedRoot builds a Worktree record for a directory containing
// a `.git` entry by invoking `git -C dir rev-parse` for the HEAD SHA and
// branch ref. Mirrors the field conventions used by parseWorktreeList:
// branch is the short ref name, "(detached)" for detached HEAD, "" if no
// commits yet exist.
func worktreeForNestedRoot(dir string) (Worktree, error) {
	wt := Worktree{Root: dir}
	if head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
		wt.HEAD = strings.TrimSpace(string(head))
	}
	if br, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(br))
		if ref == "HEAD" {
			wt.Branch = "(detached)"
		} else {
			wt.Branch = ref
		}
	}
	return wt, nil
}
