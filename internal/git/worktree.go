package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// Worktree describes a single entry from `git worktree list --porcelain`.
//
// The slice returned by ListWorktrees preserves the order git emits — the
// main worktree first, then linked worktrees in the order they were added.
// Bare repositories' bare entries are filtered out (no working tree to render).
type Worktree struct {
	Root     string // absolute working-tree path
	HEAD     string // 40-char SHA; "" for newly-added worktrees with no commits yet
	Branch   string // short ref name (no refs/heads/ prefix); "(detached)" if detached HEAD
	Locked   bool
	Prunable bool
}

// ListWorktrees enumerates the linked worktrees of the repository at repoRoot,
// including the main worktree. Bare entries are skipped. The returned slice is
// non-nil even when only the main worktree exists.
func ListWorktrees(repoRoot string) ([]Worktree, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreeList(out), nil
}

// parseWorktreeList parses `git worktree list --porcelain` output. The format
// is a sequence of key/value lines per block, blocks separated by blank lines.
// Lines are either "key" (boolean flag: bare, detached, locked, prunable) or
// "key value" (worktree, HEAD, branch). The final block may or may not be
// followed by a trailing newline.
func parseWorktreeList(b []byte) []Worktree {
	out := []Worktree{}
	cur := Worktree{}
	bare := false
	flush := func() {
		// A block with no `worktree` line is malformed — skip.
		// A bare entry has no working tree — skip.
		if cur.Root != "" && !bare {
			out = append(out, cur)
		}
		cur = Worktree{}
		bare = false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			flush()
			continue
		}
		key := line
		val := ""
		if sp := strings.IndexByte(line, ' '); sp >= 0 {
			key, val = line[:sp], line[sp+1:]
		}
		switch key {
		case "worktree":
			cur.Root = val
		case "HEAD":
			cur.HEAD = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Branch = "(detached)"
		case "bare":
			bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return out
}
