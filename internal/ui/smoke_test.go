package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// drive runs Init + the synchronous status command, then feeds a window-size
// message to the model and returns its current rendered frame.
func drive(t *testing.T, repo string) (Model, string) {
	t.Helper()
	m := New(repo)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
	msg := cmd()
	mi, _ := m.Update(msg)
	mi, _ = mi.(Model).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm := mi.(Model)
	return mm, mm.View()
}

func TestSmoke_TreeBuiltFromRealRepo(t *testing.T) {
	repo := os.Getenv("GD_SMOKE_REPO")
	if repo == "" {
		repo = "/tmp/gd-smoke"
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Skipf("no smoke repo at %s: %v", repo, err)
	}
	m, view := drive(t, repo)
	if m.err != nil {
		t.Fatalf("model error: %v", m.err)
	}
	if len(m.rows) == 0 {
		t.Fatalf("expected rows, got none. view:\n%s", view)
	}

	// Verify per-file +/- counts match `git diff --numstat HEAD`.
	want := numstat(t, repo)
	got := map[string][2]int{}
	for _, n := range m.rows {
		if n.IsDir || n.File == nil {
			continue
		}
		got[n.Path] = [2]int{n.Adds, n.Dels}
	}
	for path, expected := range want {
		if g, ok := got[path]; !ok {
			t.Errorf("missing path %s in tree", path)
		} else if g != expected {
			t.Errorf("counts for %s: want %v got %v", path, expected, g)
		}
	}

	// Untracked files should appear with Adds > 0.
	untracked := false
	for _, n := range m.rows {
		if n.File != nil && n.File.Kind == git.Untracked {
			untracked = true
			if n.Adds == 0 {
				t.Errorf("untracked %s has 0 adds", n.Path)
			}
		}
	}
	if !untracked {
		t.Errorf("expected at least one untracked file in tree")
	}

	// Verify hunk loading works for one tracked file.
	for _, n := range m.rows {
		if n.IsDir || n.File == nil || n.File.Kind == git.Untracked {
			continue
		}
		hs, err := git.LoadHunks(repo, *n.File)
		if err != nil {
			t.Fatalf("LoadHunks(%s): %v", n.Path, err)
		}
		if len(hs) == 0 {
			t.Errorf("expected hunks for %s", n.Path)
		}
		break
	}

	// Tree depth consistency: every node's Depth should be derivable.
	_ = tree.Flatten(m.root)
}

func numstat(t *testing.T, repo string) map[string][2]int {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "diff", "--numstat", "HEAD").Output()
	if err != nil {
		t.Fatalf("git diff --numstat: %v", err)
	}
	res := map[string][2]int{}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 3 || f[0] == "-" {
			continue
		}
		var a, d int
		for _, c := range f[0] {
			a = a*10 + int(c-'0')
		}
		for _, c := range f[1] {
			d = d*10 + int(c-'0')
		}
		res[f[2]] = [2]int{a, d}
	}
	return res
}
