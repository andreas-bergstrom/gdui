package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupBranchRepo creates a repo with main + a feature branch that has one
// extra commit not on main. Returns the repo path.
func setupBranchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not in PATH: %v", err)
	}
	root := resolveTempRoot(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "feature commit")
	// Return to main for the merge-base tests; feature is one commit ahead.
	run("checkout", "-q", "main")
	// And add a commit on main so feature...main has divergence.
	if err := os.WriteFile(filepath.Join(repo, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "main commit")
	return repo
}

func TestListBranches_LocalOnly(t *testing.T) {
	repo := setupBranchRepo(t)
	bs, err := ListBranches(repo)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var head string
	for _, b := range bs {
		if b.IsRemote {
			t.Errorf("unexpected remote branch: %+v", b)
		}
		names = append(names, b.Name)
		if b.IsHEAD {
			head = b.Name
		}
	}
	if head != "main" {
		t.Errorf("HEAD marker on wrong branch: %q (want main)", head)
	}
	if len(names) != 2 {
		t.Fatalf("got %v branches, want [feature main]", names)
	}
}

func TestRefDiffFiles_AgainstFeature(t *testing.T) {
	repo := setupBranchRepo(t)
	// feature...HEAD where HEAD==main: merge-base is the init commit, so the
	// diff is just the main commit (c.txt added).
	files, err := RefDiffFiles(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "c.txt" || files[0].Kind != Added {
		t.Fatalf("files=%+v, want one Added c.txt", files)
	}
	if files[0].Adds != 1 {
		t.Errorf("c.txt adds=%d, want 1", files[0].Adds)
	}
}

func TestRefDiffHunks_AgainstFeature(t *testing.T) {
	repo := setupBranchRepo(t)
	hs, err := RefDiffHunks(repo, "feature", "c.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) == 0 {
		t.Fatal("expected at least one hunk")
	}
}
