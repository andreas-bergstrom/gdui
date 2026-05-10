package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreas-bergstrom/gdui/internal/filter"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// filterState holds the active tree filter. It mirrors the rune-accumulator
// approach of searchState rather than wrapping bubbles/textinput, so the
// status row stays a single pre-rendered line and we don't pay the widget's
// extra layout machinery for a 1-line input.
//
// Lifecycle:
//   - editing == true while the user is typing in the status row.
//   - matcher != nil while a non-empty query has compiled successfully; the
//     UI consults this in flattenForView in tree modes.
//   - err != nil for an unparseable input; the status row surfaces it and
//     the matcher is left nil so the tree shows everything until the user
//     fixes the pattern.
//
// State persists across all mode transitions; non-tree modes (Log/FileLog/
// Search) simply don't call flattenForView. The only ways to clear it are
// Esc inside the input or backspace-empty + Esc.
type filterState struct {
	editing bool
	query   string
	matcher *filter.Matcher
	err     error
}

func (f *filterState) recompile() {
	m, err := filter.Compile(f.query)
	f.matcher = m
	f.err = err
}

func (f *filterState) active() bool {
	return f.matcher != nil
}

// visible reports whether the status row should be drawn at all (editing OR
// an active filter that survived edit-exit).
func (f *filterState) visible() bool {
	return f.editing || f.active() || f.err != nil
}

// flattenSectionsFiltered is the filter-aware variant of flattenSections.
// When matcher is nil it delegates so single-section / no-filter renders are
// byte-identical to the historical path.
func flattenSectionsFiltered(secs []*WorktreeSection, showHeaders bool, matcher *filter.Matcher) []displayRow {
	if matcher == nil {
		return flattenSections(secs, showHeaders)
	}
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
		for _, n := range flattenWithFilter(s.Root, matcher) {
			out = append(out, treeRow{sectionIdx: i, node: n})
		}
	}
	return out
}

func flattenCommitTreeFiltered(root *tree.Node, matcher *filter.Matcher) []displayRow {
	if root == nil {
		return nil
	}
	if matcher == nil {
		return flattenCommitTree(root)
	}
	nodes := flattenWithFilter(root, matcher)
	out := make([]displayRow, len(nodes))
	for i, n := range nodes {
		out[i] = treeRow{sectionIdx: -1, node: n}
	}
	return out
}

// flattenWithFilter walks the tree and returns the nodes that should be
// visible when a filter is active. Files are kept iff their full Path
// matches; directories are kept iff any descendant matches; matching
// directories are walked into regardless of Expanded so the user sees the
// matches without manually expanding parent dirs first.
//
// Expanded is never written — clearing the filter restores the original
// collapse state automatically.
//
// "Subtree contains a match" is memoized so a tree with N nodes is walked
// in O(N) regardless of how many directories the predicate has to consider.
func flattenWithFilter(root *tree.Node, m *filter.Matcher) []*tree.Node {
	if root == nil || m == nil {
		return nil
	}
	contains := make(map[*tree.Node]bool)
	var hasMatch func(n *tree.Node) bool
	hasMatch = func(n *tree.Node) bool {
		if v, ok := contains[n]; ok {
			return v
		}
		var v bool
		if !n.IsDir {
			v = m.Match(n.Path)
		} else {
			for _, c := range n.Children {
				if hasMatch(c) {
					v = true
					break
				}
			}
		}
		contains[n] = v
		return v
	}
	var out []*tree.Node
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		if !n.IsDir {
			if m.Match(n.Path) {
				out = append(out, n)
			}
			return
		}
		if !hasMatch(n) {
			return
		}
		out = append(out, n)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, c := range root.Children {
		walk(c)
	}
	return out
}

// handleKeyFilter dispatches keys while the filter input is editing. Returns
// tea.ClearScreen on any state-mutating key — the row count and structure
// shifts dramatically with each keystroke and bubbletea's partial-redraw
// diff misses the changes (same class of bug as the section-collapse case
// in toggleNode that already returns ClearScreen).
func (m *Model) handleKeyFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filter = filterState{}
		m.refreshView()
		return *m, tea.ClearScreen
	case tea.KeyEnter:
		m.filter.editing = false
		if m.filter.query == "" {
			m.filter = filterState{}
		}
		m.refreshView()
		return *m, tea.ClearScreen
	case tea.KeyBackspace, tea.KeyCtrlH:
		if len(m.filter.query) > 0 {
			r := []rune(m.filter.query)
			m.filter.query = string(r[:len(r)-1])
			m.filter.recompile()
			m.refreshView()
		}
		return *m, tea.ClearScreen
	case tea.KeyCtrlU:
		if len(m.filter.query) > 0 {
			m.filter.query = ""
			m.filter.recompile()
			m.refreshView()
		}
		return *m, tea.ClearScreen
	}
	if len(msg.Runes) > 0 {
		m.filter.query += string(msg.Runes)
		m.filter.recompile()
		m.refreshView()
		return *m, tea.ClearScreen
	}
	return *m, nil
}

// enterFilterEdit starts editing mode. If a filter is already active it's
// pre-loaded into the input so re-pressing `f` lets the user refine the
// existing query rather than starting from scratch.
func (m *Model) enterFilterEdit() tea.Cmd {
	m.filter.editing = true
	if m.filter.matcher == nil && m.filter.query != "" {
		m.filter.recompile()
	}
	m.refreshView()
	return tea.ClearScreen
}

var (
	filterPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#bb9af7")).Bold(true)
	filterCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5")).Reverse(true)
)

// renderFilterStatus returns the one-line status row drawn between the
// viewport and the footer. Format:
//
//	filter: <query>[█]                              <N files | err>
//
// If the line would exceed m.width, the query is truncated with a leading
// `…` so the user keeps seeing what they're typing.
func (m *Model) renderFilterStatus() string {
	prompt := filterPromptStyle.Render("filter: ")
	queryDisplay := m.filter.query
	cursor := ""
	if m.filter.editing {
		cursor = filterCursorStyle.Render(" ")
	}

	var right string
	switch {
	case m.filter.err != nil:
		right = errStyle.Render("err: " + m.filter.err.Error())
	case m.filter.active():
		hint := ""
		if !m.filter.editing {
			hint = " · esc clear"
		}
		right = helpStyle.Render(fmt.Sprintf("%d files", m.countVisibleFiles()) + hint)
	case m.filter.editing:
		right = helpStyle.Render("type to filter · enter accept · esc cancel")
	}

	leftRaw := prompt + fileStyle.Render(queryDisplay) + cursor
	if m.width <= 0 {
		return leftRaw + "  " + right
	}

	rightW := lipgloss.Width(right)
	leftW := lipgloss.Width(leftRaw)
	const gap = 2
	if leftW+gap+rightW > m.width {
		// Trim the query from the LEFT (newest text stays visible). Compute
		// how many runes of query we can afford after subtracting prompt,
		// cursor, gap, right.
		promptW := lipgloss.Width(prompt)
		cursorW := lipgloss.Width(cursor)
		avail := m.width - promptW - cursorW - gap - rightW - 1 // 1 for `…`
		if avail < 1 {
			avail = 1
		}
		runes := []rune(queryDisplay)
		if len(runes) > avail {
			queryDisplay = "…" + string(runes[len(runes)-avail:])
		}
		leftRaw = prompt + fileStyle.Render(queryDisplay) + cursor
		leftW = lipgloss.Width(leftRaw)
	}
	pad := m.width - leftW - rightW
	if pad < gap {
		pad = gap
	}
	return leftRaw + strings.Repeat(" ", pad) + right
}

// countVisibleFiles returns the number of treeRow entries in m.rows that
// represent files (not directories). Used by the filter status row's "<N>
// files" indicator.
func (m *Model) countVisibleFiles() int {
	n := 0
	for _, r := range m.rows {
		if t, ok := r.(treeRow); ok && t.node != nil && !t.node.IsDir {
			n++
		}
	}
	return n
}
