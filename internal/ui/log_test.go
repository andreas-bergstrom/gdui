package ui

import (
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
