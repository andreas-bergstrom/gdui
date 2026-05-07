package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/andreasbergstrom/gd/internal/git"
	"github.com/andreasbergstrom/gd/internal/render"
	"github.com/andreasbergstrom/gd/internal/tree"
)

type Model struct {
	repoRoot string
	root     *tree.Node
	rows     []*tree.Node
	cursor   int
	vp       viewport.Model
	width    int
	height   int
	err      error
	zones    *zone.Manager
	showHelp bool
	ready    bool
	allMode  bool // false = changed only; true = full repo tree, unchanged dimmed
}

func New(repoRoot string) Model {
	return Model{
		repoRoot: repoRoot,
		root:     &tree.Node{IsDir: true, Expanded: true},
		zones:    zone.New(),
	}
}

func (m Model) Init() tea.Cmd {
	return loadStatusCmd(m.repoRoot, m.allMode)
}

// --- messages ---

// RefreshMsg can be sent externally (e.g. from a file watcher) to trigger a
// reload of the diff state.
type RefreshMsg struct{}

type statusMsg struct {
	root *tree.Node
	err  error
}

type hunksMsg struct {
	path  string
	hunks []git.Hunk
	err   error
}

func loadStatusCmd(root string, allMode bool) tea.Cmd {
	return func() tea.Msg {
		files, err := git.Status(root)
		if err != nil {
			return statusMsg{err: err}
		}
		if !allMode {
			return statusMsg{root: tree.Build(files)}
		}
		all, err := git.ListAll(root)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{root: tree.BuildAll(files, all)}
	}
}

func loadHunksCmd(repoRoot, path string, file git.ChangedFile) tea.Cmd {
	return func() tea.Msg {
		hs, err := git.LoadHunks(repoRoot, file)
		return hunksMsg{path: path, hunks: hs, err: err}
	}
}

// --- update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerH, footerH := 1, 1
		vpH := msg.Height - headerH - footerH
		if vpH < 1 {
			vpH = 1
		}
		if !m.ready {
			m.vp = viewport.New(msg.Width, vpH)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = vpH
		}
		m.refreshView()
		return m, nil

	case RefreshMsg:
		return m, loadStatusCmd(m.repoRoot, m.allMode)

	case statusMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.preserveStateInto(msg.root)
		m.root = msg.root
		m.cursor = 0
		m.refreshView()
		return m, nil

	case hunksMsg:
		// Resolve by path against the current tree — the tree may have been
		// rebuilt by a refresh while the hunk load was in flight, in which
		// case the original *tree.Node pointer would be stale.
		if n := tree.FindByPath(m.root, msg.path); n != nil && !n.IsDir {
			n.Loading = false
			n.LoadErr = msg.err
			n.Hunks = msg.hunks
			// If the user collapsed the row before the hunks arrived, leave
			// the loaded data attached but don't force expansion.
			m.refreshView()
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Refresh):
		return m, loadStatusCmd(m.repoRoot, m.allMode)
	case key.Matches(msg, keys.ToggleAll):
		m.allMode = !m.allMode
		return *m, loadStatusCmd(m.repoRoot, m.allMode)
	case key.Matches(msg, keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, keys.Down):
		m.moveCursor(1)
	case key.Matches(msg, keys.Up):
		m.moveCursor(-1)
	case key.Matches(msg, keys.PgDn):
		m.moveCursor(m.vp.Height / 2)
	case key.Matches(msg, keys.PgUp):
		m.moveCursor(-m.vp.Height / 2)
	case key.Matches(msg, keys.NextDir):
		m.jumpDir(1)
	case key.Matches(msg, keys.PrevDir):
		m.jumpDir(-1)
	case key.Matches(msg, keys.Top):
		m.cursor = 0
		m.refreshView()
	case key.Matches(msg, keys.Bottom):
		m.cursor = len(m.rows) - 1
		m.refreshView()
	case key.Matches(msg, keys.Toggle):
		return *m, m.toggle(m.currentNode())
	case key.Matches(msg, keys.Right):
		n := m.currentNode()
		if n != nil && !n.Expanded {
			return *m, m.toggle(n)
		}
	case key.Matches(msg, keys.Left):
		n := m.currentNode()
		if n == nil {
			return *m, nil
		}
		if n.Expanded {
			return *m, m.toggle(n)
		}
		// jump to parent
		if n.Parent != nil && n.Parent.Parent != nil {
			for i, r := range m.rows {
				if r == n.Parent {
					m.cursor = i
					break
				}
			}
			m.refreshView()
		}
	}
	return *m, nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.vp.LineUp(3)
		return *m, nil
	case tea.MouseButtonWheelDown:
		m.vp.LineDown(3)
		return *m, nil
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return *m, nil
	}
	for i, n := range m.rows {
		if n == nil {
			continue
		}
		if z := m.zones.Get(zoneID(i)); z.InBounds(msg) {
			m.cursor = i
			return *m, m.toggle(n)
		}
	}
	return *m, nil
}

// --- helpers ---

func (m *Model) currentNode() *tree.Node {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

func (m *Model) toggle(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	if n.IsDir {
		n.Expanded = !n.Expanded
		m.refreshView()
		return nil
	}
	// unchanged file in all-mode: nothing to expand
	if n.File == nil {
		return nil
	}
	// file
	if n.Expanded {
		n.Expanded = false
		n.Hunks = nil // drop on collapse per design
		n.LoadErr = nil
		m.refreshView()
		return nil
	}
	n.Expanded = true
	if n.File != nil && n.File.Binary {
		m.refreshView()
		return nil
	}
	n.Loading = true
	m.refreshView()
	return loadHunksCmd(m.repoRoot, n.Path, *n.File)
}

// jumpDir moves the cursor to the next (dir=+1) or previous (dir=-1) folder
// row, skipping over file rows entirely.
func (m *Model) jumpDir(dir int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor + dir
	for i >= 0 && i < len(m.rows) {
		if m.rows[i].IsDir {
			m.cursor = i
			m.refreshView()
			return
		}
		i += dir
	}
}

func (m *Model) moveCursor(d int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	m.refreshView()
}

// preserveStateInto copies expand state from old tree into the new one by path.
func (m *Model) preserveStateInto(newRoot *tree.Node) {
	if m.root == nil {
		return
	}
	old := map[string]bool{}
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		if n.Path != "" {
			old[n.Path] = n.Expanded
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(m.root)

	var apply func(n *tree.Node)
	apply = func(n *tree.Node) {
		if v, ok := old[n.Path]; ok && n.Path != "" {
			n.Expanded = v
		}
		for _, c := range n.Children {
			apply(c)
		}
	}
	apply(newRoot)
}

func (m *Model) refreshView() {
	if !m.ready {
		return
	}
	m.rows = tree.Flatten(m.root)
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.vp.SetContent(m.renderBody())
	m.ensureCursorVisible()
}

// We approximate cursor Y by counting lines up to the cursor row (each tree
// row is 1 line; expanded files add their hunk line count).
func (m *Model) ensureCursorVisible() {
	y := 0
	for i, n := range m.rows {
		if i == m.cursor {
			break
		}
		y++
		if !n.IsDir && n.Expanded {
			y += hunkLineCount(n)
		}
	}
	if y < m.vp.YOffset {
		m.vp.SetYOffset(y)
	} else if y >= m.vp.YOffset+m.vp.Height {
		m.vp.SetYOffset(y - m.vp.Height + 1)
	}
}

func hunkLineCount(n *tree.Node) int {
	if n.File != nil && n.File.Binary {
		return 1
	}
	if n.Loading {
		return 1
	}
	if n.LoadErr != nil {
		return 1
	}
	c := 0
	for _, h := range n.Hunks {
		c++ // header
		c += len(h.Lines)
		for _, l := range h.Lines {
			if l.NoNewlineHere {
				c++
			}
		}
	}
	if c == 0 {
		c = 1 // "no diff" placeholder
	}
	return c
}

// --- view ---

func (m Model) View() string {
	if !m.ready {
		return ""
	}
	header := m.header()
	footer := m.footer()
	body := m.vp.View()
	return m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, header, body, footer))
}

func (m Model) header() string {
	title := headerStyle.Render(" gd ")
	repo := dimStyle.Render(m.repoRoot)
	return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", repo)
}

func (m Model) footer() string {
	if m.showHelp {
		return helpStyle.Render("j/k move · [/] jump folder · enter toggle · h/l collapse/expand · g/G top/bot · ^d/^u page · a all/changed · r refresh · q quit")
	}
	if m.err != nil {
		return errStyle.Render("error: " + m.err.Error())
	}
	changed, adds, dels := 0, 0, 0
	if m.root != nil {
		adds, dels = m.root.Adds, m.root.Dels
	}
	for _, n := range m.rows {
		if !n.IsDir && n.File != nil {
			changed++
		}
	}
	mode := "changed"
	if m.allMode {
		mode = "all"
	}
	left := fmt.Sprintf("[%s] %d changed", mode, changed)
	totals := addsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", dels))
	return helpStyle.Render(left+" · ") + totals + helpStyle.Render(" · a toggle · ? help")
}

func (m *Model) renderBody() string {
	if len(m.rows) == 0 {
		if m.err != nil {
			return errStyle.Render(m.err.Error())
		}
		return dimStyle.Render("  no changes")
	}
	var b strings.Builder
	for i, n := range m.rows {
		row := m.renderRow(i, n)
		b.WriteString(m.zones.Mark(zoneID(i), row))
		b.WriteByte('\n')
		if !n.IsDir && n.Expanded {
			b.WriteString(m.renderExpanded(n))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderRow(i int, n *tree.Node) string {
	depth := tree.Depth(n)
	indent := strings.Repeat("  ", depth)
	chev := " "
	if n.IsDir || n.File != nil {
		if n.Expanded {
			chev = "▾"
		} else {
			chev = "▸"
		}
	}
	var name string
	switch {
	case n.IsDir:
		if n.Interesting {
			name = dirStyle.Render(n.Name + "/")
		} else {
			name = dimStyle.Render(n.Name + "/")
		}
	case n.File != nil:
		k := n.File.Kind.Letter()[0]
		marker := kindStyle[k].Render(string(k))
		nm := fileStyle.Render(n.Name)
		if n.File.Kind == git.Renamed && n.File.OldPath != "" {
			nm += dimStyle.Render(" ← " + n.File.OldPath)
		}
		name = marker + " " + nm
	default:
		// unchanged file (all-mode)
		name = "  " + dimStyle.Render(n.Name)
	}
	counts := ""
	if n.Adds > 0 || n.Dels > 0 {
		counts = " " + addsStyle.Render(fmt.Sprintf("+%d", n.Adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", n.Dels))
	}
	left := fmt.Sprintf("%s%s %s", indent, chev, name)
	row := left + counts
	if i == m.cursor {
		// highlight whole visible width
		pad := m.width - lipgloss.Width(row)
		if pad > 0 {
			row += strings.Repeat(" ", pad)
		}
		row = cursorStyle.Render(row)
	}
	return row
}

func (m *Model) renderExpanded(n *tree.Node) string {
	if n.File != nil && n.File.Binary {
		return dimStyle.Render("  ⟨binary file⟩")
	}
	if n.Loading {
		return dimStyle.Render("  loading…")
	}
	if n.LoadErr != nil {
		return errStyle.Render("  " + n.LoadErr.Error())
	}
	if len(n.Hunks) == 0 {
		return dimStyle.Render("  ⟨no diff⟩")
	}
	return render.Hunks(n.Path, n.Hunks, m.width)
}

func zoneID(i int) string { return fmt.Sprintf("row-%d", i) }
