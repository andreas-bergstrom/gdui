package git

import "testing"

func TestParsePorcelainV2_Records(t *testing.T) {
	out := []byte("1 .M N... 100644 100644 100644 aaaa bbbb b.txt\x00" +
		"2 R. N... 100644 100644 100644 cccc dddd R100 new.txt\x00old.txt\x00" +
		"? foo.txt\x00")
	files := parsePorcelainV2(out)
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "b.txt" || files[0].Kind != Modified {
		t.Errorf("ordinary: %+v", files[0])
	}
	if files[1].Path != "new.txt" || files[1].OldPath != "old.txt" || files[1].Kind != Renamed {
		t.Errorf("rename: %+v", files[1])
	}
	if files[2].Path != "foo.txt" || files[2].Kind != Untracked {
		t.Errorf("untracked: %+v", files[2])
	}
}

func TestParsePorcelainV2_UntrackedPathsPreservedByteForByte(t *testing.T) {
	// The parser must not editorialise paths. A nested repo / in-tree linked
	// worktree arrives as an opaque directory with a trailing slash — the UI
	// layer (nestedChildPathsMap + filterChangedFiles) is what drops it, so
	// only repos that actually own a section disappear from the parent.
	// A filename that is itself whitespace ("foo/ ") must survive untouched.
	out := []byte("? .claude/worktrees/inner/\x00? foo/ \x00")
	files := parsePorcelainV2(out)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != ".claude/worktrees/inner/" {
		t.Errorf("trailing-slash entry mangled: %q", files[0].Path)
	}
	if files[1].Path != "foo/ " {
		t.Errorf("whitespace basename mangled: %q", files[1].Path)
	}
}

func TestParsePorcelainV2_UnmergedRecordIsConflicted(t *testing.T) {
	// "u XY sub m1 m2 m3 mW h1 h2 h3 path" — a file with merge conflicts.
	out := []byte("u UU N... 100644 100644 100644 100644 7898 ba29 2299 f.txt\x00? other.txt\x00")
	files := parsePorcelainV2(out)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "f.txt" || files[0].Kind != Conflicted {
		t.Errorf("unmerged: %+v", files[0])
	}
	if Conflicted.Letter() != "U" {
		t.Errorf("Conflicted letter = %q, want U", Conflicted.Letter())
	}
}
