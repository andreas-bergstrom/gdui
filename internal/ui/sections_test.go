package ui

import (
	"testing"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

func mkSection(root, branch string, files ...git.ChangedFile) *WorktreeSection {
	return &WorktreeSection{
		WT:       git.Worktree{Root: root, Branch: branch},
		Files:    files,
		Root:     tree.Build(files),
		Expanded: true,
	}
}

func TestFlattenSections_NoHeadersWhenSingle(t *testing.T) {
	s := mkSection("/repo/main", "main",
		git.ChangedFile{Path: "a.go", Kind: git.Modified},
	)
	rows := flattenSections([]*WorktreeSection{s}, false)
	for _, r := range rows {
		if _, ok := r.(headerRow); ok {
			t.Errorf("single-section view should emit no header rows")
		}
	}
	if len(rows) == 0 {
		t.Fatal("expected tree rows for single section")
	}
}

func TestFlattenSections_HeadersPrefixEachSection(t *testing.T) {
	a := mkSection("/repo/a", "main",
		git.ChangedFile{Path: "a.go", Kind: git.Modified},
	)
	b := mkSection("/repo/b", "feat",
		git.ChangedFile{Path: "b.go", Kind: git.Modified},
	)
	rows := flattenSections([]*WorktreeSection{a, b}, true)
	if len(rows) < 4 { // 2 headers + at least 1 row per section
		t.Fatalf("expected at least 4 rows, got %d", len(rows))
	}
	if h, ok := rows[0].(headerRow); !ok || h.sectionIdx != 0 {
		t.Errorf("rows[0] should be header for section 0; got %T %+v", rows[0], rows[0])
	}
	// Find the second header — should mark sectionIdx 1.
	found := false
	for _, r := range rows[1:] {
		if h, ok := r.(headerRow); ok {
			if h.sectionIdx != 1 {
				t.Errorf("second header should be sectionIdx=1, got %d", h.sectionIdx)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("did not find second section header")
	}
}

func TestFlattenSections_CollapsedSectionContributesOnlyHeader(t *testing.T) {
	a := mkSection("/repo/a", "main",
		git.ChangedFile{Path: "a.go", Kind: git.Modified},
	)
	a.Expanded = false
	b := mkSection("/repo/b", "feat",
		git.ChangedFile{Path: "b.go", Kind: git.Modified},
	)
	rows := flattenSections([]*WorktreeSection{a, b}, true)
	// Section a is collapsed: only its header. Section b: header + tree rows.
	headers := 0
	treeRowsForA := 0
	for _, r := range rows {
		switch row := r.(type) {
		case headerRow:
			_ = row
			headers++
		case treeRow:
			if row.sectionIdx == 0 {
				treeRowsForA++
			}
		}
	}
	if headers != 2 {
		t.Errorf("expected 2 headers, got %d", headers)
	}
	if treeRowsForA != 0 {
		t.Errorf("collapsed section should contribute 0 tree rows, got %d", treeRowsForA)
	}
}

func TestFlattenSections_TreeRowsCarrySectionIdx(t *testing.T) {
	a := mkSection("/repo/a", "main",
		git.ChangedFile{Path: "a.go", Kind: git.Modified},
	)
	b := mkSection("/repo/b", "feat",
		git.ChangedFile{Path: "b.go", Kind: git.Modified},
	)
	rows := flattenSections([]*WorktreeSection{a, b}, true)
	for _, r := range rows {
		if tr, ok := r.(treeRow); ok {
			if tr.sectionIdx < 0 || tr.sectionIdx > 1 {
				t.Errorf("unexpected sectionIdx %d on tree row", tr.sectionIdx)
			}
		}
	}
}

func TestFindSectionByRoot(t *testing.T) {
	secs := []*WorktreeSection{
		{WT: git.Worktree{Root: "/repo/a"}},
		{WT: git.Worktree{Root: "/repo/b"}},
	}
	if findSectionByRoot(secs, "/repo/b") != 1 {
		t.Errorf("expected idx 1")
	}
	if findSectionByRoot(secs, "/repo/missing") != -1 {
		t.Errorf("expected -1 for missing root (drop-stale-message case)")
	}
	if findSectionByRoot(nil, "/repo/a") != -1 {
		t.Errorf("expected -1 for nil sections")
	}
}

func TestFlattenCommitTree_NoHeader(t *testing.T) {
	root := tree.Build([]git.ChangedFile{
		{Path: "a.go", Kind: git.Modified},
	})
	rows := flattenCommitTree(root)
	for _, r := range rows {
		if _, ok := r.(headerRow); ok {
			t.Errorf("commit-tree flatten should never emit headers")
		}
		if tr, ok := r.(treeRow); ok && tr.sectionIdx != -1 {
			t.Errorf("commit-tree rows should carry sectionIdx=-1, got %d", tr.sectionIdx)
		}
	}
}

func TestNeedsRepaintAfterMove(t *testing.T) {
	h0 := headerRow{sectionIdx: 0}
	h1 := headerRow{sectionIdx: 1}
	t0a := treeRow{sectionIdx: 0}
	t0b := treeRow{sectionIdx: 0}
	t1a := treeRow{sectionIdx: 1}

	cases := []struct {
		name string
		prev displayRow
		next displayRow
		want bool
	}{
		{"same row", t0a, t0a, false},
		{"two tree rows in same section", t0a, t0b, false},
		{"header to tree, same section", h0, t0a, true},
		{"tree to header, same section", t0a, h0, true},
		{"tree across sections", t0a, t1a, true},
		{"header across sections", h0, h1, true},
		{"nil prev", nil, t0a, false},
		{"nil next", t0a, nil, false},
	}
	for _, c := range cases {
		if got := needsRepaintAfterMove(c.prev, c.next); got != c.want {
			t.Errorf("%s: needsRepaintAfterMove = %v, want %v", c.name, got, c.want)
		}
	}
}
