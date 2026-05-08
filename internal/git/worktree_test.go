package git

import "testing"

func TestParseWorktreeList_Empty(t *testing.T) {
	got := parseWorktreeList(nil)
	if len(got) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(got))
	}
	got = parseWorktreeList([]byte(""))
	if len(got) != 0 {
		t.Errorf("expected 0 worktrees on empty input, got %d", len(got))
	}
}

func TestParseWorktreeList_SingleMain(t *testing.T) {
	in := []byte(`worktree /Users/andreas/Projekt/ui
HEAD a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2
branch refs/heads/main

`)
	got := parseWorktreeList(in)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	w := got[0]
	if w.Root != "/Users/andreas/Projekt/ui" || w.HEAD != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" || w.Branch != "main" {
		t.Errorf("fields: %+v", w)
	}
	if w.Locked || w.Prunable {
		t.Errorf("flags should be false: %+v", w)
	}
}

func TestParseWorktreeList_MainAndLinked(t *testing.T) {
	in := []byte(`worktree /repo/main
HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
branch refs/heads/main

worktree /repo/wt-feat
HEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
branch refs/heads/feat-auth

worktree /repo/wt-detach
HEAD cccccccccccccccccccccccccccccccccccccccc
detached
`)
	got := parseWorktreeList(in)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Root != "/repo/main" || got[0].Branch != "main" {
		t.Errorf("main: %+v", got[0])
	}
	if got[1].Root != "/repo/wt-feat" || got[1].Branch != "feat-auth" {
		t.Errorf("linked: %+v", got[1])
	}
	if got[2].Root != "/repo/wt-detach" || got[2].Branch != "(detached)" {
		t.Errorf("detached: %+v", got[2])
	}
}

func TestParseWorktreeList_LockedAndPrunable(t *testing.T) {
	in := []byte(`worktree /repo/main
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /repo/locked
HEAD 2222222222222222222222222222222222222222
branch refs/heads/locked-branch
locked

worktree /repo/old
HEAD 3333333333333333333333333333333333333333
branch refs/heads/old
prunable gitdir file points to non-existent location
`)
	got := parseWorktreeList(in)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if !got[1].Locked {
		t.Errorf("expected locked: %+v", got[1])
	}
	if !got[2].Prunable {
		t.Errorf("expected prunable: %+v", got[2])
	}
	// Ensure the prunable reason after the space doesn't leak into other fields.
	if got[2].Branch != "old" {
		t.Errorf("branch on prunable: %q", got[2].Branch)
	}
}

func TestParseWorktreeList_SkipsBare(t *testing.T) {
	in := []byte(`worktree /repo/bare
bare

worktree /repo/wt
HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
branch refs/heads/main
`)
	got := parseWorktreeList(in)
	if len(got) != 1 {
		t.Fatalf("got %d (bare should be skipped), want 1: %+v", len(got), got)
	}
	if got[0].Root != "/repo/wt" {
		t.Errorf("expected non-bare entry, got %+v", got[0])
	}
}

func TestParseWorktreeList_NoTrailingBlankLine(t *testing.T) {
	// Some git versions / locales may omit the final blank separator.
	in := []byte("worktree /repo/main\nHEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbranch refs/heads/main")
	got := parseWorktreeList(in)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (final block must be flushed)", len(got))
	}
}

func TestParseWorktreeList_MalformedKeyOnlyLines(t *testing.T) {
	// Forward-compatibility: future git versions might add new keys we don't
	// recognise. Unknown keys should be silently ignored, not crash.
	in := []byte(`worktree /repo/main
HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
branch refs/heads/main
some-future-flag
some-future-key with a value

`)
	got := parseWorktreeList(in)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Branch != "main" {
		t.Errorf("unknown keys leaked into branch: %+v", got[0])
	}
}

func TestParseWorktreeList_BlockWithoutWorktreeLineIgnored(t *testing.T) {
	// A block that somehow lacks a `worktree` line is malformed; drop it.
	in := []byte(`HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
branch refs/heads/orphan

worktree /repo/real
HEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
branch refs/heads/main
`)
	got := parseWorktreeList(in)
	if len(got) != 1 || got[0].Root != "/repo/real" {
		t.Fatalf("expected only the real worktree, got %+v", got)
	}
}
