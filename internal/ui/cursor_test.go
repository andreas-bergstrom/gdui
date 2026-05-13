package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

func driveMultiSection(t *testing.T) Model {
	t.Helper()
	a := mkSection("/repo/master", "master",
		git.ChangedFile{Path: "gdui-dev", Kind: git.Untracked, Adds: 65497},
	)
	b := mkSection("/repo/bugfix", "bugfix",
		git.ChangedFile{Path: "Readme.md", Kind: git.Modified, Adds: 3, Dels: 3},
		git.ChangedFile{Path: "scratch.tmp", Kind: git.Untracked, Adds: 1},
	)
	c := mkSection("/repo/feat", "feat",
		git.ChangedFile{Path: "Readme.md", Kind: git.Modified, Adds: 2},
	)
	a.firstLoadDone = true
	b.firstLoadDone = true
	c.firstLoadDone = true

	m := New("/repo/master", nil)
	m.zones = zone.New()
	m.sections = []*WorktreeSection{a, b, c}
	m.activeWT = 0
	mi, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return mi.(Model)
}

func cursorRowDesc(m Model) string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return "(out of bounds)"
	}
	switch r := m.rows[m.cursor].(type) {
	case headerRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return "header:" + m.sections[r.sectionIdx].WT.Branch
		}
		return "header:?"
	case treeRow:
		if r.node == nil {
			return "tree:nil"
		}
		section := "?"
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			section = m.sections[r.sectionIdx].WT.Branch
		}
		return "tree:" + section + "/" + r.node.Path
	}
	return "?"
}

// Regression: a status refresh arriving while the cursor is on a section
// header used to reset cursor to row 0 because cursorPath() returned ""
// for header rows. The fix anchors on the section's worktree root.
func TestStatusMsg_PreservesCursorOnSectionHeader(t *testing.T) {
	m := driveMultiSection(t)

	for i, r := range m.rows {
		if h, ok := r.(headerRow); ok && h.sectionIdx == 1 {
			m.cursor = i
			break
		}
	}
	wantDesc := cursorRowDesc(m)

	mi, _ := m.Update(statusMsg{
		root:  "/repo/master",
		files: []git.ChangedFile{{Path: "gdui-dev", Kind: git.Untracked, Adds: 65497}},
		tree:  tree.Build([]git.ChangedFile{{Path: "gdui-dev", Kind: git.Untracked, Adds: 65497}}),
	})
	m = mi.(Model)

	if got := cursorRowDesc(m); got != wantDesc {
		t.Errorf("cursor moved on statusMsg while on header: want %s, got %s", wantDesc, got)
	}
}

// Regression: same path in different sections. Cursor on feat/Readme.md
// must not snap to bugfix/Readme.md (which the old path-only lookup would
// have done since bugfix appears first in the row list).
func TestStatusMsg_DistinguishesSamePathAcrossSections(t *testing.T) {
	m := driveMultiSection(t)

	for i, r := range m.rows {
		t2, ok := r.(treeRow)
		if !ok || t2.node == nil {
			continue
		}
		if t2.sectionIdx == 2 && t2.node.Path == "Readme.md" {
			m.cursor = i
			break
		}
	}
	wantDesc := cursorRowDesc(m)

	mi, _ := m.Update(statusMsg{
		root: "/repo/feat",
		files: []git.ChangedFile{
			{Path: "Readme.md", Kind: git.Modified, Adds: 2},
		},
		tree: tree.Build([]git.ChangedFile{
			{Path: "Readme.md", Kind: git.Modified, Adds: 2},
		}),
	})
	m = mi.(Model)

	if got := cursorRowDesc(m); got != wantDesc {
		t.Errorf("cursor jumped to wrong section's same-named file: want %s, got %s", wantDesc, got)
	}
}

// When a tree row vanishes (file deleted from disk between status reloads)
// the cursor falls back to that section's header rather than to row 0.
func TestStatusMsg_FallbackToSectionHeaderWhenFileGone(t *testing.T) {
	m := driveMultiSection(t)

	for i, r := range m.rows {
		t2, ok := r.(treeRow)
		if !ok || t2.node == nil {
			continue
		}
		if t2.sectionIdx == 1 && t2.node.Path == "scratch.tmp" {
			m.cursor = i
			break
		}
	}

	mi, _ := m.Update(statusMsg{
		root: "/repo/bugfix",
		files: []git.ChangedFile{
			{Path: "Readme.md", Kind: git.Modified, Adds: 3, Dels: 3},
		},
		tree: tree.Build([]git.ChangedFile{
			{Path: "Readme.md", Kind: git.Modified, Adds: 3, Dels: 3},
		}),
	})
	m = mi.(Model)

	got := cursorRowDesc(m)
	if got != "header:bugfix" {
		t.Errorf("cursor should fall back to bugfix section header when scratch.tmp is gone: got %s", got)
	}
}
