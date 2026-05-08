package ui

import (
	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// WorktreeSection groups one git worktree's view state inside Model.
//
// In ModeChanged/ModeAll, every section is rendered as its own tree, with a
// collapsible header when len(sections) > 1. In ModeLog/ModeCommit/ModeFileLog,
// sections only determine which worktree is "active" — only the active
// section's commit log / commit tree is shown.
type WorktreeSection struct {
	WT       git.Worktree
	Files    []git.ChangedFile // raw, retained for header counts
	Root     *tree.Node        // built tree (Changed/All mode); nil while loading
	Expanded bool              // section-header collapse state
	LoadErr  error

	// firstLoadDone toggles after the first statusMsg arrives, so we can
	// auto-expand sections that have changes the first time we see them
	// without overriding subsequent user-driven collapses.
	firstLoadDone bool
}

// displayRow is a row in the unified row list rendered by Model.View.
// Concrete types: headerRow (section header), treeRow (tree node), commitRow
// (commit log entry).
type displayRow interface{ isDisplayRow() }

type headerRow struct{ sectionIdx int }
type treeRow struct {
	sectionIdx int // -1 for ModeCommit (commit-tree rows belong to the active selection, not a section)
	node       *tree.Node
}
type commitRow struct{ idx int }

func (headerRow) isDisplayRow() {}
func (treeRow) isDisplayRow()   {}
func (commitRow) isDisplayRow() {}

// flattenSections produces the display row list for tree-mode views (Changed
// / All). When showHeaders is false (single section), only tree rows are
// emitted. When true, each section's tree is preceded by a header row, and
// collapsed sections contribute only their header.
func flattenSections(secs []*WorktreeSection, showHeaders bool) []displayRow {
	out := []displayRow{}
	for i, s := range secs {
		if showHeaders {
			out = append(out, headerRow{sectionIdx: i})
			if !s.Expanded {
				continue
			}
		}
		if s.Root == nil {
			continue
		}
		for _, n := range tree.Flatten(s.Root) {
			out = append(out, treeRow{sectionIdx: i, node: n})
		}
	}
	return out
}

// flattenCommitTree produces a displayRow list for ModeCommit, where the
// commit's file tree is shown without any section header.
func flattenCommitTree(root *tree.Node) []displayRow {
	if root == nil {
		return nil
	}
	nodes := tree.Flatten(root)
	out := make([]displayRow, len(nodes))
	for i, n := range nodes {
		out[i] = treeRow{sectionIdx: -1, node: n}
	}
	return out
}

// findSectionByRoot returns the index of the section with the given working-
// tree root, or -1 if none. Used to route async messages back to the
// originating section after possibly-reordering refreshes.
func findSectionByRoot(secs []*WorktreeSection, root string) int {
	for i, s := range secs {
		if s.WT.Root == root {
			return i
		}
	}
	return -1
}

// asTreeRow returns the *tree.Node when r is a treeRow, otherwise nil.
func asTreeRow(r displayRow) *tree.Node {
	if t, ok := r.(treeRow); ok {
		return t.node
	}
	return nil
}

// sectionHasChanges reports whether the section has any non-empty changes
// after status loads. Used to decide auto-expand on first load.
func sectionHasChanges(s *WorktreeSection) bool {
	return len(s.Files) > 0
}
