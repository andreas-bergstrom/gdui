package watch

import (
	"path/filepath"
	"testing"
)

func TestShouldIgnore_GitDir(t *testing.T) {
	repo := "/repo"
	if !shouldIgnore(repo, filepath.Join(repo, ".git", "objects", "abc")) {
		t.Error(".git contents should be ignored")
	}
	if !shouldIgnore(repo, filepath.Join(repo, ".git")) {
		t.Error(".git itself should be ignored")
	}
}

func TestShouldIgnore_AllowsLogsHEAD(t *testing.T) {
	repo := "/repo"
	headPath := filepath.Join(repo, ".git", "logs", "HEAD")
	if shouldIgnore(repo, headPath) {
		t.Errorf(".git/logs/HEAD should NOT be ignored: %s", headPath)
	}
}

func TestShouldIgnore_VendorAndNodeModules(t *testing.T) {
	repo := "/repo"
	cases := []string{
		filepath.Join(repo, "node_modules", "foo", "index.js"),
		filepath.Join(repo, "vendor", "bar.go"),
	}
	for _, p := range cases {
		if !shouldIgnore(repo, p) {
			t.Errorf("expected ignore: %s", p)
		}
	}
}

func TestShouldIgnore_EditorSwapFiles(t *testing.T) {
	repo := "/repo"
	cases := []string{
		filepath.Join(repo, "src", ".#main.go"),       // emacs lock
		filepath.Join(repo, "src", "main.go~"),         // backup
		filepath.Join(repo, "src", ".main.go.swp"),     // vim primary swap
		filepath.Join(repo, "src", ".main.go.swo"),     // vim secondary swap
		filepath.Join(repo, "src", ".main.go.swx"),     // vim crash recovery
	}
	for _, p := range cases {
		if !shouldIgnore(repo, p) {
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
		if shouldIgnore(repo, p) {
			t.Errorf("regular file should not be ignored: %s", p)
		}
	}
}

func TestShouldIgnore_RootIsIgnored(t *testing.T) {
	if !shouldIgnore("/repo", "/repo") {
		t.Error("repo root itself should be ignored (rel='.')")
	}
}
