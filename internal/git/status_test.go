package git

import "testing"

func TestParsePorcelainV2_DropsUntrackedNestedRepoDirs(t *testing.T) {
	// git emits a nested repo / linked worktree inside the tree as a single
	// untracked entry with a trailing slash, even under --untracked-files=all,
	// because it refuses to descend into it. Such an entry is not a file and
	// must not become a tree leaf.
	out := []byte("? .claude/worktrees/inner/\x00? foo.txt\x00")
	files := parsePorcelainV2(out)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d: %+v", len(files), files)
	}
	if files[0].Path != "foo.txt" || files[0].Kind != Untracked {
		t.Errorf("got %+v", files[0])
	}
}
