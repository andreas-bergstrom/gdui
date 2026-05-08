package watch

import (
	"path/filepath"
	"testing"
)

func TestShouldIgnore_GitDir(t *testing.T) {
	repo := "/repo"
	if !shouldIgnore(repo, filepath.Join(repo, ".git", "objects", "abc"), "") {
		t.Error(".git contents should be ignored")
	}
	if !shouldIgnore(repo, filepath.Join(repo, ".git"), "") {
		t.Error(".git itself should be ignored")
	}
}

func TestShouldIgnore_AllowsConfiguredHeadLogPath_InsideRepoRoot(t *testing.T) {
	// Main worktree: HEAD log lives at <repoRoot>/.git/logs/HEAD.
	repo := "/repo"
	headPath := filepath.Join(repo, ".git", "logs", "HEAD")
	if shouldIgnore(repo, headPath, headPath) {
		t.Errorf("configured headLogPath should NOT be ignored: %s", headPath)
	}
}

func TestShouldIgnore_AllowsConfiguredHeadLogPath_OutsideRepoRoot(t *testing.T) {
	// Linked worktree: HEAD log lives outside the working tree, under
	// <main>/.git/worktrees/<name>/logs/HEAD. filepath.Rel produces a
	// `..`-prefixed result, but the explicit equality check must still
	// admit the file.
	repo := "/repo/wt-feat"
	headLogPath := "/repo/main/.git/worktrees/wt-feat/logs/HEAD"
	if shouldIgnore(repo, headLogPath, headLogPath) {
		t.Errorf("configured headLogPath should NOT be ignored even when outside repoRoot: %s", headLogPath)
	}
}

func TestShouldIgnore_SiblingFilesInLogDirAreIgnored(t *testing.T) {
	// We watch the parent dir of headLogPath (fsnotify can only watch
	// directories), but sibling files in there must still be ignored —
	// only the exact HEAD-log path is meant to fire a refresh.
	repo := "/repo/wt-feat"
	headLogPath := "/repo/main/.git/worktrees/wt-feat/logs/HEAD"
	sibling := "/repo/main/.git/worktrees/wt-feat/logs/refs"
	if !shouldIgnore(repo, sibling, headLogPath) {
		t.Errorf("sibling file in same parent dir as headLogPath should be ignored: %s", sibling)
	}
}

func TestShouldIgnore_VendorAndNodeModules(t *testing.T) {
	repo := "/repo"
	cases := []string{
		filepath.Join(repo, "node_modules", "foo", "index.js"),
		filepath.Join(repo, "vendor", "bar.go"),
	}
	for _, p := range cases {
		if !shouldIgnore(repo, p, "") {
			t.Errorf("expected ignore: %s", p)
		}
	}
}

func TestShouldIgnore_EditorSwapFiles(t *testing.T) {
	repo := "/repo"
	cases := []string{
		filepath.Join(repo, "src", ".#main.go"),    // emacs lock
		filepath.Join(repo, "src", "main.go~"),     // backup
		filepath.Join(repo, "src", ".main.go.swp"), // vim primary swap
		filepath.Join(repo, "src", ".main.go.swo"), // vim secondary swap
		filepath.Join(repo, "src", ".main.go.swx"), // vim crash recovery
	}
	for _, p := range cases {
		if !shouldIgnore(repo, p, "") {
			t.Errorf("expected ignore: %s", p)
		}
	}
}

func TestShouldIgnore_AllowsRegularFiles(t *testing.T) {
	repo := "/repo"
	cases := []string{
		filepath.Join(repo, "src", "main.go"),
		filepath.Join(repo, "README.md"),
		filepath.Join(repo, "internal", "git", "diff.go"),
	}
	for _, p := range cases {
		if shouldIgnore(repo, p, "") {
			t.Errorf("regular file should not be ignored: %s", p)
		}
	}
}

func TestShouldIgnore_RootIsIgnored(t *testing.T) {
	if !shouldIgnore("/repo", "/repo", "") {
		t.Error("repo root itself should be ignored (rel='.')")
	}
}

func TestShouldIgnore_PathsOutsideRepoRootAreIgnored(t *testing.T) {
	// Without a configured headLogPath, paths that resolve outside
	// repoRoot should be ignored — fsnotify on macOS occasionally surfaces
	// canonical /private/-prefixed events that resolve "above" /tmp/foo.
	repo := "/repo"
	if !shouldIgnore(repo, "/elsewhere/file.go", "") {
		t.Error("path outside repoRoot should be ignored")
	}
}
