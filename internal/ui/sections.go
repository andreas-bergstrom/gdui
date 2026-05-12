package ui

import (
	"path/filepath"
	"strings"

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

	// Nested is true when this section represents a nested git repo
	// (independent repo or submodule) discovered by walking a parent
	// worktree, rather than a linked worktree returned by `git worktree
	// list`. The header renders a path label so users can tell sections
	// apart when branch names collide.
	Nested bool
	// Label is the display string prefixed to the header for nested
	// sections — the nested repo's path relative to its parent worktree
	// (or filepath.Base of its root as a fallback). Empty for non-nested
	// sections.
	Label string

	// firstLoadDone toggles after the first statusMsg arrives, so we can
	// auto-expand sections that have changes the first time we see them
	// without overriding subsequent user-driven collapses.
	firstLoadDone bool

	// Per-section commit-log state. Populated by ModeLog's lazy paged
	// loader (loadLogCmd + reloadSectionLog). Independent of Files/Root
	// above so collapse state and tree state are not entangled with log
	// state.
	//
	// LogCommits is newest-first (matches `git log`); pages are appended
	// as the user scrolls. LogReloadGen is incremented on every reset
	// (manual refresh, watcher commit) so stale in-flight pages from a
	// superseded generation are silently dropped — closes the
	// pagination-during-refresh race.
	LogCommits    []git.Commit
	LogLoaded     bool
	LogLoading    bool
	LogHasMore    bool
	LogReloadGen  int
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
type commitRow struct {
	sectionIdx int // -1 when no section context (e.g. flat ModeFileLog)
	idx        int // index into the owning section's LogCommits
}

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

// flattenSectionsLogs produces the display row list for ModeLog. Mirrors
// flattenSections but emits commitRow entries from each section's
// LogCommits rather than tree nodes. When showHeaders is false (single-
// section repo), only commit rows are emitted — byte-identical to the
// pre-multi-section single-pane log layout.
//
// Collapsed sections contribute only their header (no commits) so users
// can lazily expand the ones they care about; the load itself is kicked
// elsewhere when the header is expanded.
func flattenSectionsLogs(secs []*WorktreeSection, showHeaders bool) []displayRow {
	out := []displayRow{}
	for i, s := range secs {
		if showHeaders {
			out = append(out, headerRow{sectionIdx: i})
			if !s.Expanded {
				continue
			}
		}
		for j := range s.LogCommits {
			out = append(out, commitRow{sectionIdx: i, idx: j})
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

// nestedSectionLabel returns the path of `nestedRoot` relative to the
// longest entry in `parentRoots` that is a proper ancestor of it. Falls
// back to filepath.Base on the nested root when no parent matches (e.g.
// gdui launched from inside a nested repo so the parent is unknown).
//
// Picking the longest matching parent ensures that, if nested repos are
// themselves nested under another nested repo, the label is short and
// relative to the *closest* parent — not the launch root.
func nestedSectionLabel(nestedRoot string, parentRoots []string) string {
	nestedRoot = filepath.Clean(nestedRoot)
	best := ""
	for _, p := range parentRoots {
		p = filepath.Clean(p)
		if !strings.HasPrefix(nestedRoot, p+string(filepath.Separator)) {
			continue
		}
		if len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return filepath.Base(nestedRoot)
	}
	rel, err := filepath.Rel(best, nestedRoot)
	if err != nil {
		return filepath.Base(nestedRoot)
	}
	return rel
}
