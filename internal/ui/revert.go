package ui

import (
	"fmt"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// revertPhase enumerates the revert UI states. revertIdle means no prompt
// is drawn; revertPromptConfirm renders the y/n status row and swallows keys
// via handleKeyRevert.
type revertPhase int

const (
	revertIdle revertPhase = iota
	revertPromptConfirm
)

// revertState backs the "revert to HEAD" flow. files is frozen at prompt
// time so the operation runs against the snapshot the user confirmed, not
// whatever the tree looks like by the time the cmd executes.
type revertState struct {
	phase revertPhase
	root  string
	files []git.ChangedFile
	label string
	err   string
}

func (r *revertState) visible() bool { return r.phase != revertIdle || r.err != "" }
func (r *revertState) active() bool  { return r.phase != revertIdle }

type revertCompletedMsg struct{ root string }
type revertFailedMsg struct{ err error }

// initRevert resolves the cursor target, collects the changed files under
// it, and transitions to the confirmation prompt. Returns nil (no cmd) when
// the cursor isn't on a tree row or no changed files are reachable from it.
func (m *Model) initRevert() tea.Cmd {
	if m.mode != ModeChanged && m.mode != ModeAll {
		return nil
	}
	r, ok := m.currentRow().(treeRow)
	if !ok || r.node == nil {
		return nil
	}
	if r.sectionIdx < 0 || r.sectionIdx >= len(m.sections) {
		return nil
	}
	files := collectRevertFiles(r.node)
	if len(files) == 0 {
		return nil
	}
	root := m.sections[r.sectionIdx].WT.Root
	m.revert = revertState{
		phase: revertPromptConfirm,
		root:  root,
		files: files,
		label: revertLabel(r.node, len(files)),
	}
	m.refreshView()
	return tea.ClearScreen
}

// collectRevertFiles returns every changed file at or under node. For leaf
// rows it's a single file; for directories it's a DFS over Children. Leaves
// with File==nil (unchanged files in ModeAll) are skipped.
func collectRevertFiles(node *tree.Node) []git.ChangedFile {
	var out []git.ChangedFile
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		if n == nil {
			return
		}
		if !n.IsDir {
			if n.File != nil {
				out = append(out, *n.File)
			}
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(node)
	return out
}

func revertLabel(node *tree.Node, n int) string {
	if !node.IsDir {
		return node.Path
	}
	base := node.Path
	if base == "" {
		base = "/"
	}
	return fmt.Sprintf("%s/ (%d files)", base, n)
}

// handleKeyRevert dispatches keys while the confirmation prompt is up.
// Enter/y/Y confirms; esc/n/N cancels. Any other key is swallowed so global
// bindings (q, r, a, …) can't fire mid-prompt.
func (m *Model) handleKeyRevert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	confirm := func() (tea.Model, tea.Cmd) {
		root := m.revert.root
		files := m.revert.files
		m.revert = revertState{}
		m.refreshView()
		return *m, tea.Batch(revertCmd(root, files), tea.ClearScreen)
	}
	cancel := func() (tea.Model, tea.Cmd) {
		m.revert = revertState{}
		m.refreshView()
		return *m, tea.ClearScreen
	}
	if msg.Type == tea.KeyEnter {
		return confirm()
	}
	if msg.Type == tea.KeyEsc {
		return cancel()
	}
	// Require !msg.Paste so a clipboard "y" can't accidentally confirm a
	// destructive op. Mirrors the same guard in handleKeyDropOverwrite.
	if len(msg.Runes) == 1 && !msg.Paste {
		switch msg.Runes[0] {
		case 'y', 'Y':
			return confirm()
		case 'n', 'N':
			return cancel()
		}
	}
	return *m, nil
}

// revertCmd applies RevertFile sequentially. Git's index isn't safe for
// concurrent writes from the same repo, so this is intentionally serial; the
// file watcher's 200ms debounce absorbs the burst of fsnotify events the
// operations generate.
func revertCmd(root string, files []git.ChangedFile) tea.Cmd {
	return func() tea.Msg {
		for _, f := range files {
			if err := git.RevertFile(root, f); err != nil {
				return revertFailedMsg{err: fmt.Errorf("%s: %w", f.Path, err)}
			}
		}
		return revertCompletedMsg{root: root}
	}
}

// revertResetOnModeChange clears revert state on mode transitions out of
// tree modes — silent resume on a later return would be surprising. Mirrors
// dropResetOnModeChange and is called from the same callsites.
func (m *Model) revertResetOnModeChange() {
	if m.revert.phase == revertIdle && m.revert.err == "" {
		return
	}
	m.revert = revertState{}
}

var revertPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")).Bold(true)

// renderRevertStatus draws the bottom status row when the prompt is up or
// the last revert errored. Returns "" when idle and no sticky error.
func (m *Model) renderRevertStatus() string {
	if m.revert.phase == revertPromptConfirm {
		prompt := revertPromptStyle.Render("revert: ")
		body := fileStyle.Render(filepath.ToSlash(m.revert.label)) + fileStyle.Render("? (y/n)")
		left := prompt + body
		right := helpStyle.Render("enter=yes · esc=cancel")
		return m.padToWidth(left, right)
	}
	if m.revert.err != "" {
		left := revertPromptStyle.Render("revert: ") + errStyle.Render(m.revert.err)
		right := helpStyle.Render("esc dismiss")
		return m.padToWidth(left, right)
	}
	return ""
}
