package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RevertFile reverts a single file in worktree `root` to its state at HEAD.
//
//	Modified, Deleted,
//	Conflicted          -> git checkout HEAD -- <path>  (discards both staged
//	                       and unstaged changes in favor of HEAD)
//	Renamed             -> git checkout HEAD -- <oldPath>; git rm -f -- <newPath>
//	Added (staged-new)  -> git rm -f -- <path>  (drops index entry + file)
//	Untracked           -> os.Remove(<root>/<path>)
//
// Not transactional: callers batching across multiple files should retry on
// failure (operations are idempotent).
func RevertFile(root string, f ChangedFile) error {
	switch f.Kind {
	case Modified, Deleted, Conflicted:
		return runGit(root, "checkout", "HEAD", "--", f.Path)
	case Renamed:
		if f.OldPath == "" {
			return fmt.Errorf("renamed file %q missing OldPath", f.Path)
		}
		if err := runGit(root, "checkout", "HEAD", "--", f.OldPath); err != nil {
			return err
		}
		return runGit(root, "rm", "-f", "--", f.Path)
	case Added:
		return runGit(root, "rm", "-f", "--", f.Path)
	case Untracked:
		full := filepath.Join(root, f.Path)
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown change kind for %q", f.Path)
	}
}

func runGit(root string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, string(out))
	}
	return nil
}
