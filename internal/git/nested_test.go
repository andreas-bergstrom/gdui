package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// gitInit runs the standard sequence to make `dir` a git repo with one
// committed file so HEAD/branch resolve. Skips the test if `git` is missing.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not in PATH: %v", err)
	}
	mustRun := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// Force a deterministic identity so commits work in CI sandboxes.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "README")
	mustRun("commit", "-q", "-m", "init")
}

// resolveTempRoot returns the temp dir with symlinks resolved. macOS's
// /tmp -> /private/tmp link otherwise makes filepath.Rel produce surprising
// results in assertions.
func resolveTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

func TestDiscoverNestedRepos_IndependentNested(t *testing.T) {
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	gitInit(t, parent)
	nested := filepath.Join(parent, "nested-a")
	gitInit(t, nested)

	got, err := DiscoverNestedRepos(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d nested repos, want 1: %+v", len(got), got)
	}
	if got[0].Root != nested {
		t.Errorf("Root = %q, want %q", got[0].Root, nested)
	}
	if got[0].Branch != "main" {
		t.Errorf("Branch = %q, want main", got[0].Branch)
	}
	if len(got[0].HEAD) != 40 {
		t.Errorf("HEAD length = %d, want 40 (got %q)", len(got[0].HEAD), got[0].HEAD)
	}
}

func TestDiscoverNestedRepos_Submodule(t *testing.T) {
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	donor := filepath.Join(root, "donor")
	gitInit(t, parent)
	gitInit(t, donor)

	// Add `donor` as a submodule at parent/sub. Need protocol.file.allow=always
	// on newer git versions which block file:// submodule sources by default.
	cmd := exec.Command("git", "-C", parent,
		"-c", "protocol.file.allow=always",
		"submodule", "add", "-q", donor, "sub")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("submodule add failed (likely sandboxed git config): %v: %s", err, out)
	}

	// Confirm parent/sub/.git is a regular file (gitlink), not a directory —
	// the case our walk needs to handle.
	info, err := os.Lstat(filepath.Join(parent, "sub", ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatalf(".git is a directory, expected a gitlink file")
	}

	got, err := DiscoverNestedRepos(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(parent, "sub")
	found := false
	for _, w := range got {
		if w.Root == want {
			found = true
			if w.Branch == "" && w.HEAD == "" {
				t.Errorf("submodule fields not populated: %+v", w)
			}
		}
	}
	if !found {
		t.Errorf("submodule at %q not in results: %+v", want, got)
	}
}

func TestDiscoverNestedRepos_SkipsIgnoredDirs(t *testing.T) {
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	gitInit(t, parent)
	// Stash a repo under each ignored directory name — none should be returned.
	for _, ignored := range []string{"node_modules", "vendor"} {
		gitInit(t, filepath.Join(parent, ignored, "pkg"))
	}
	// And one real nested repo to anchor the assertion.
	real := filepath.Join(parent, "real")
	gitInit(t, real)

	got, err := DiscoverNestedRepos(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	roots := make([]string, len(got))
	for i, w := range got {
		roots[i] = w.Root
	}
	sort.Strings(roots)
	if len(roots) != 1 || roots[0] != real {
		t.Errorf("ignored-dir repos leaked into results: %v", roots)
	}
}

func TestDiscoverNestedRepos_DoesNotDescendIntoNested(t *testing.T) {
	// Single call must not return nested-inside-nested — caller's job.
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	gitInit(t, parent)
	a := filepath.Join(parent, "a")
	gitInit(t, a)
	gitInit(t, filepath.Join(a, "deep"))

	got, err := DiscoverNestedRepos(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Root != a {
		t.Fatalf("expected only direct nested repo, got %+v", got)
	}
}

func TestDiscoverNestedReposRecursive_FindsNestedInsideNested(t *testing.T) {
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	gitInit(t, parent)
	a := filepath.Join(parent, "a")
	gitInit(t, a)
	deep := filepath.Join(a, "deep")
	gitInit(t, deep)

	seeds := []Worktree{{Root: parent}}
	got := DiscoverNestedReposRecursive(seeds, 0)

	roots := make([]string, 0, len(got))
	for _, w := range got {
		roots = append(roots, w.Root)
	}
	sort.Strings(roots)
	want := []string{a, deep}
	if len(roots) != 2 || roots[0] != want[0] || roots[1] != want[1] {
		t.Errorf("recursive discovery roots = %v, want %v", roots, want)
	}
}

func TestDiscoverNestedRepos_SymlinkLoopDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on Windows")
	}
	root := resolveTempRoot(t)
	parent := filepath.Join(root, "parent")
	gitInit(t, parent)
	// Create a symlink that points back to the parent. filepath.WalkDir does
	// not follow symlinks, so this should be a no-op — but a regression in
	// either WalkDir behavior or our own code would loop forever, hanging
	// the test (and the user's gdui session). Test serves as a tripwire.
	if err := os.Symlink(parent, filepath.Join(parent, "loopback")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err := DiscoverNestedRepos(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
}
