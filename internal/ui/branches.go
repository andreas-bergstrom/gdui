package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// branchPickerState holds the overlay's state while the user picks a ref.
// Independent from search/filter — coexists with neither (entering the picker
// is gated on tree modes only).
type branchPickerState struct {
	prevMode viewMode
	root     string // worktree root the picker is acting on

	query  string
	cursor int

	all      []git.Branch // full list as returned by ListBranches
	filtered []git.Branch // narrowed by query

	loaded bool
	err    error
}

// branchDiffState holds the active branch-diff view (ModeBranchDiff).
// Mirrors ModeCommit's pattern: a single tree against a specific ref.
type branchDiffState struct {
	root     string // worktree the diff belongs to
	ref      string // short branch name, e.g. "main" or "origin/main"
	tr       *tree.Node
	prevMode viewMode // mode to return to on esc (ModeChanged or ModeAll)
}

// --- messages ---

type branchListMsg struct {
	root     string
	branches []git.Branch
	err      error
}

type branchTreeMsg struct {
	root  string
	ref   string
	files []git.ChangedFile
	tr    *tree.Node
	err   error
}

// --- commands ---

func loadBranchesCmd(root string) tea.Cmd {
	return func() tea.Msg {
		bs, err := git.ListBranches(root)
		return branchListMsg{root: root, branches: bs, err: err}
	}
}

func loadBranchTreeCmd(root, ref string) tea.Cmd {
	return func() tea.Msg {
		files, err := git.RefDiffFiles(root, ref)
		if err != nil {
			return branchTreeMsg{root: root, ref: ref, err: err}
		}
		t := tree.Build(files)
		return branchTreeMsg{root: root, ref: ref, files: files, tr: t}
	}
}

func loadBranchHunksCmd(root, ref, path string) tea.Cmd {
	return func() tea.Msg {
		hs, err := git.RefDiffHunks(root, ref, path)
		return hunksMsg{root: root, ref: ref, path: path, hunks: hs, err: err}
	}
}

// --- entry / exit ---

// enterBranchPicker opens the overlay for picking a ref to diff against. The
// scope is the cursor's owning section if any (so tab-jumping is respected),
// falling back to the active worktree.
func (m *Model) enterBranchPicker() tea.Cmd {
	if m.mode != ModeChanged && m.mode != ModeAll {
		return nil
	}
	root := m.currentSectionRoot()
	if root == "" {
		root = m.activeRoot()
	}
	if root == "" {
		return nil
	}
	m.dropResetOnModeChange()
	m.revertResetOnModeChange()
	m.branchPicker = branchPickerState{
		prevMode: m.mode,
		root:     root,
	}
	m.mode = ModeBranchPicker
	m.refreshView()
	return loadBranchesCmd(root)
}

func (m *Model) exitBranchPicker() tea.Cmd {
	prev := m.branchPicker.prevMode
	if prev != ModeChanged && prev != ModeAll {
		prev = ModeChanged
	}
	m.mode = prev
	m.branchPicker = branchPickerState{}
	m.refreshView()
	return nil
}

// confirmBranchPick switches into ModeBranchDiff for the selected branch.
func (m *Model) confirmBranchPick() tea.Cmd {
	if m.branchPicker.cursor < 0 || m.branchPicker.cursor >= len(m.branchPicker.filtered) {
		return nil
	}
	b := m.branchPicker.filtered[m.branchPicker.cursor]
	root := m.branchPicker.root
	prev := m.branchPicker.prevMode
	if prev != ModeChanged && prev != ModeAll {
		prev = ModeChanged
	}
	m.branchDiff = branchDiffState{root: root, ref: b.Name, prevMode: prev}
	m.branchPicker = branchPickerState{}
	m.mode = ModeBranchDiff
	m.cursor = 0
	m.diffCursor = -1
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadBranchTreeCmd(root, b.Name)
}

// exitBranchDiff returns to the tree mode that was active before the picker
// opened, preserved on branchDiffState.prevMode. Defaults to ModeChanged
// defensively when the stored value isn't a tree mode.
func (m *Model) exitBranchDiff() tea.Cmd {
	prev := m.branchDiff.prevMode
	if prev != ModeChanged && prev != ModeAll {
		prev = ModeChanged
	}
	m.mode = prev
	m.branchDiff = branchDiffState{}
	m.cursor = 0
	m.diffCursor = -1
	m.vp.SetYOffset(0)
	m.refreshView()
	return nil
}

// --- filtering ---

// refilterBranches recomputes m.branchPicker.filtered from .all using the
// current query as a case-insensitive substring match. Resets cursor.
func (m *Model) refilterBranches() {
	q := strings.ToLower(strings.TrimSpace(m.branchPicker.query))
	if q == "" {
		m.branchPicker.filtered = append([]git.Branch(nil), m.branchPicker.all...)
	} else {
		out := make([]git.Branch, 0, len(m.branchPicker.all))
		for _, b := range m.branchPicker.all {
			if strings.Contains(strings.ToLower(b.Name), q) {
				out = append(out, b)
			}
		}
		m.branchPicker.filtered = out
	}
	m.branchPicker.cursor = 0
}

// --- key handling ---

func (m *Model) handleKeyBranchPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		return *m, m.exitBranchPicker()
	case tea.KeyEnter:
		return *m, m.confirmBranchPick()
	case tea.KeyUp:
		if m.branchPicker.cursor > 0 {
			m.branchPicker.cursor--
			m.refreshView()
		}
		return *m, nil
	case tea.KeyDown:
		if m.branchPicker.cursor < len(m.branchPicker.filtered)-1 {
			m.branchPicker.cursor++
			m.refreshView()
		}
		return *m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		if len(m.branchPicker.query) > 0 {
			r := []rune(m.branchPicker.query)
			m.branchPicker.query = string(r[:len(r)-1])
			m.refilterBranches()
			m.refreshView()
		}
		return *m, nil
	case tea.KeyCtrlU:
		m.branchPicker.query = ""
		m.refilterBranches()
		m.refreshView()
		return *m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.branchPicker.query += string(msg.Runes)
		m.refilterBranches()
		m.refreshView()
	}
	return *m, nil
}

// --- view ---

// renderBranchPicker draws the overlay: a short header, the query line, then
// a vertical list with the cursor highlighted. Width-bounded by the viewport.
func (m *Model) renderBranchPicker() string {
	w := m.vp.Width
	if w <= 0 {
		w = m.width
	}
	if w <= 0 {
		w = 80
	}
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Render("branch diff")
	scope := lipgloss.NewStyle().Faint(true).Render(" · " + shortLabelFor(m.branchPicker.root, m.sections))
	b.WriteString(title)
	b.WriteString(scope)
	b.WriteString("\n")

	prompt := "› " + m.branchPicker.query
	cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
	b.WriteString(prompt + cursor + "\n\n")

	if m.branchPicker.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("error: " + m.branchPicker.err.Error()))
		return b.String()
	}
	if !m.branchPicker.loaded {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render("loading branches…"))
		return b.String()
	}
	if len(m.branchPicker.filtered) == 0 {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render("no matching branches"))
		return b.String()
	}

	// Visible window: at most viewport height - header lines.
	maxRows := m.vp.Height - 4
	if maxRows < 5 {
		maxRows = 5
	}
	start := 0
	if m.branchPicker.cursor >= maxRows {
		start = m.branchPicker.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.branchPicker.filtered) {
		end = len(m.branchPicker.filtered)
	}

	sel := lipgloss.NewStyle().Reverse(true)
	remote := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	head := lipgloss.NewStyle().Faint(true)
	for i := start; i < end; i++ {
		br := m.branchPicker.filtered[i]
		marker := "  "
		if br.IsHEAD {
			marker = "* "
		}
		name := br.Name
		if br.IsRemote {
			name = remote.Render(name)
		}
		line := marker + name
		if br.IsHEAD {
			line += head.Render("  (current)")
		}
		if i == m.branchPicker.cursor {
			line = sel.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// shortLabelFor returns a short display label for a worktree root in the
// context of the loaded sections — branch name when known, else basename.
func shortLabelFor(root string, sections []*WorktreeSection) string {
	for _, s := range sections {
		if s.WT.Root == root {
			if s.WT.Branch != "" {
				return s.WT.Branch
			}
		}
	}
	// Fall back to the last path segment.
	if i := strings.LastIndex(root, "/"); i >= 0 {
		return root[i+1:]
	}
	return root
}
