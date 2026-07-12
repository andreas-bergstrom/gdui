package ui

import (
	"strings"
	"testing"

	"github.com/andreas-bergstrom/gdui/internal/git"
)

func mkLogSection(root string, expanded bool, commits ...git.Commit) *WorktreeSection {
	return &WorktreeSection{
		WT:         git.Worktree{Root: root, Branch: "main"},
		Expanded:   expanded,
		LogCommits: commits,
		LogLoaded:  true,
	}
}

func TestFlattenSectionsLogs_SingleSectionNoHeaders(t *testing.T) {
	s := mkLogSection("/repo", true,
		git.Commit{SHA: "a", Subject: "one"},
		git.Commit{SHA: "b", Subject: "two"},
	)
	rows := flattenSectionsLogs([]*WorktreeSection{s}, false)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (no header in single-section)", len(rows))
	}
	for _, r := range rows {
		if _, ok := r.(headerRow); ok {
			t.Errorf("header row leaked into single-section flatten")
		}
	}
}

func TestFlattenSectionsLogs_MultiSectionWithHeaders(t *testing.T) {
	a := mkLogSection("/repo/a", true, git.Commit{SHA: "1"}, git.Commit{SHA: "2"})
	b := mkLogSection("/repo/b", true, git.Commit{SHA: "3"})
	rows := flattenSectionsLogs([]*WorktreeSection{a, b}, true)
	// 2 headers + 3 commits
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	if _, ok := rows[0].(headerRow); !ok {
		t.Errorf("row[0] should be header, got %T", rows[0])
	}
	if c, ok := rows[1].(commitRow); !ok || c.sectionIdx != 0 || c.idx != 0 {
		t.Errorf("row[1] = %+v, want commitRow{0, 0}", rows[1])
	}
	if c, ok := rows[2].(commitRow); !ok || c.sectionIdx != 0 || c.idx != 1 {
		t.Errorf("row[2] = %+v, want commitRow{0, 1}", rows[2])
	}
	if _, ok := rows[3].(headerRow); !ok {
		t.Errorf("row[3] should be header, got %T", rows[3])
	}
	if c, ok := rows[4].(commitRow); !ok || c.sectionIdx != 1 || c.idx != 0 {
		t.Errorf("row[4] = %+v, want commitRow{1, 0}", rows[4])
	}
}

func TestFlattenSectionsLogs_CollapsedSectionEmitsOnlyHeader(t *testing.T) {
	a := mkLogSection("/repo/a", false, git.Commit{SHA: "1"}, git.Commit{SHA: "2"})
	b := mkLogSection("/repo/b", true, git.Commit{SHA: "3"})
	rows := flattenSectionsLogs([]*WorktreeSection{a, b}, true)
	// Collapsed `a` contributes only its header. Expanded `b` contributes
	// header + 1 commit. Total: 3.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if c, ok := rows[2].(commitRow); !ok || c.sectionIdx != 1 {
		t.Errorf("row[2] = %+v, want commit from section 1", rows[2])
	}
}

func TestCommitPushMarker(t *testing.T) {
	s := &WorktreeSection{
		WT:           git.Worktree{Root: "/repo", Branch: "main"},
		HasRemotes:   true,
		UnpushedSHAs: map[string]bool{"unpushed-sha": true},
	}
	m := &Model{sections: []*WorktreeSection{s}}

	if got := m.commitPushMarker("/repo", "unpushed-sha"); !strings.Contains(got, "⇡") {
		t.Errorf("unpushed commit marker = %q, want to contain ⇡", got)
	}
	if got := m.commitPushMarker("/repo", "pushed-sha"); strings.Contains(got, "⇡") {
		t.Errorf("pushed commit marker = %q, want no ⇡", got)
	}
	// No remotes → never marked, even if in the set.
	s.HasRemotes = false
	if got := m.commitPushMarker("/repo", "unpushed-sha"); strings.Contains(got, "⇡") {
		t.Errorf("no-remote marker = %q, want no ⇡", got)
	}
	// Unknown root → two spaces, no panic.
	if got := m.commitPushMarker("/other", "unpushed-sha"); strings.Contains(got, "⇡") {
		t.Errorf("unknown-root marker = %q, want no ⇡", got)
	}
}

func TestRenderCommitRowAt_ShowsUnpushedMarker(t *testing.T) {
	s := &WorktreeSection{
		WT:           git.Worktree{Root: "/repo", Branch: "main"},
		HasRemotes:   true,
		UnpushedSHAs: map[string]bool{"aaa": true},
	}
	m := &Model{sections: []*WorktreeSection{s}, width: 80, cursor: -1}
	unpushed := m.renderCommitRowAt(0, git.Commit{SHA: "aaa", ShortSHA: "aaa", Subject: "local"}, "/repo")
	if !strings.Contains(unpushed, "⇡") {
		t.Errorf("unpushed row = %q, want ⇡ marker", unpushed)
	}
	pushed := m.renderCommitRowAt(1, git.Commit{SHA: "bbb", ShortSHA: "bbb", Subject: "remote"}, "/repo")
	if strings.Contains(pushed, "⇡") {
		t.Errorf("pushed row = %q, want no ⇡ marker", pushed)
	}
}

func TestUnpushedCrumb(t *testing.T) {
	a := &WorktreeSection{HasRemotes: true, UnpushedCount: 2}
	b := &WorktreeSection{HasRemotes: true, UnpushedCount: 3}
	m := Model{sections: []*WorktreeSection{a, b}}
	if got := m.unpushedCrumb(); !strings.Contains(got, "⇡5 unpushed") {
		t.Errorf("crumb = %q, want ⇡5 unpushed", got)
	}

	// All pushed → empty crumb.
	a.UnpushedCount, b.UnpushedCount = 0, 0
	if got := m.unpushedCrumb(); got != "" {
		t.Errorf("crumb with 0 unpushed = %q, want empty", got)
	}

	// Unpushed exists but no remotes → suppressed.
	a.UnpushedCount = 4
	a.HasRemotes = false
	b.HasRemotes = false
	if got := m.unpushedCrumb(); got != "" {
		t.Errorf("crumb with no remotes = %q, want empty", got)
	}
}

func TestMaybeAutoLoadMore_TriggersNearEnd(t *testing.T) {
	// Section with 12 commits, has more available. Cursor on idx 9 (within
	// the threshold-of-3 window of idx 11 = last) → should fire a load.
	s := &WorktreeSection{
		WT:         git.Worktree{Root: "/repo"},
		Expanded:   true,
		LogLoaded:  true,
		LogHasMore: true,
	}
	for i := 0; i < 12; i++ {
		s.LogCommits = append(s.LogCommits, git.Commit{SHA: string(rune('a' + i))})
	}
	m := &Model{
		sections: []*WorktreeSection{s},
		mode:     ModeLog,
	}
	m.rows = flattenSectionsLogs(m.sections, false)
	m.cursor = 9
	cmd := m.maybeAutoLoadMore()
	if cmd == nil {
		t.Fatalf("expected a tea.Cmd from auto-load-more; got nil")
	}
	if !s.LogLoading {
		t.Errorf("LogLoading should be set to true by auto-load-more")
	}
}

func TestMaybeAutoLoadMore_NotTriggeredFarFromEnd(t *testing.T) {
	s := &WorktreeSection{
		WT:         git.Worktree{Root: "/repo"},
		Expanded:   true,
		LogLoaded:  true,
		LogHasMore: true,
	}
	for i := 0; i < 12; i++ {
		s.LogCommits = append(s.LogCommits, git.Commit{SHA: string(rune('a' + i))})
	}
	m := &Model{sections: []*WorktreeSection{s}, mode: ModeLog}
	m.rows = flattenSectionsLogs(m.sections, false)
	m.cursor = 2
	cmd := m.maybeAutoLoadMore()
	if cmd != nil {
		t.Errorf("did not expect a load at cursor 2/12; got cmd=%v", cmd)
	}
	if s.LogLoading {
		t.Errorf("LogLoading should remain false when not auto-loading")
	}
}

func TestMaybeAutoLoadMore_NoMoreToLoad(t *testing.T) {
	// LogHasMore=false: even at the very end, no load.
	s := &WorktreeSection{
		WT:         git.Worktree{Root: "/repo"},
		Expanded:   true,
		LogLoaded:  true,
		LogHasMore: false,
		LogCommits: []git.Commit{{SHA: "a"}, {SHA: "b"}},
	}
	m := &Model{sections: []*WorktreeSection{s}, mode: ModeLog}
	m.rows = flattenSectionsLogs(m.sections, false)
	m.cursor = 1
	if cmd := m.maybeAutoLoadMore(); cmd != nil {
		t.Errorf("did not expect a load when LogHasMore=false; got cmd=%v", cmd)
	}
}
