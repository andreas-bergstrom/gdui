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

const logLimit = 100

type viewMode int

const (
	ModeChanged viewMode = iota
	ModeAll
	ModeLog
	ModeCommit
)

func (m viewMode) String() string {
	switch m {
	case ModeAll:
		return "all"
	case ModeLog:
		return "log"
	case ModeCommit:
		return "commit"
	default:
		return "changed"
	}
}

type Model struct {
	repoRoot string
	mode     viewMode

	// tree state — used in Changed/All/Commit modes
	root   *tree.Node
	rows   []*tree.Node
	cursor int

	// log state — used in Log mode
	commits      []git.Commit
	commitCursor int
	logLoaded    bool // true once a logMsg has been processed (distinguishes "loading" from "empty repo")

	// commit drill-in state — set when mode == ModeCommit
	selectedSHA     string
	selectedShort   string
	selectedSubject string

	vp       viewport.Model
	width    int
	height   int
	err      error
	zones    *zone.Manager
	showHelp bool
	ready    bool
}

func New(repoRoot string) Model {
	return Model{
		repoRoot: repoRoot,
		mode:     ModeChanged,
		root:     &tree.Node{IsDir: true, Expanded: true},
		zones:    zone.New(),
	}
}

func (m Model) Init() tea.Cmd {
	return loadStatusCmd(m.repoRoot, false)
}

// --- messages ---

// RefreshMsg can be sent externally (e.g. from a file watcher) to trigger a
// reload of the diff state. Ignored in Log/Commit modes.
type RefreshMsg struct{}

type statusMsg struct {
	root *tree.Node
	err  error
}

type logMsg struct {
	commits []git.Commit
	err     error
}

type commitTreeMsg struct {
	sha  string
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

func loadLogCmd(root string) tea.Cmd {
	return func() tea.Msg {
		c, err := git.Log(root, logLimit)
		return logMsg{commits: c, err: err}
	}
}

func loadCommitTreeCmd(root, sha string) tea.Cmd {
	return func() tea.Msg {
		files, err := git.CommitFiles(root, sha)
		if err != nil {
			return commitTreeMsg{sha: sha, err: err}
		}
		return commitTreeMsg{sha: sha, root: tree.Build(files)}
	}
}

func loadHunksCmd(repoRoot, path string, file git.ChangedFile, sha string) tea.Cmd {
	return func() tea.Msg {
		var hs []git.Hunk
		var err error
		if sha == "" {
			hs, err = git.LoadHunks(repoRoot, file)
		} else {
			hs, err = git.CommitHunks(repoRoot, sha, file.Path)
		}
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
		// File-watcher triggered refresh: only meaningful for working-tree views.
		if m.mode == ModeChanged || m.mode == ModeAll {
			return m, loadStatusCmd(m.repoRoot, m.mode == ModeAll)
		}
		return m, nil

	case statusMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		// Preserve the cursor's path across reloads so a watcher-triggered
		// refresh while editing doesn't yank the cursor back to the top.
		var prevPath string
		if c := m.currentNode(); c != nil {
			prevPath = c.Path
		}
		m.preserveStateInto(msg.root)
		m.root = msg.root
		m.rows = tree.Flatten(m.root)
		m.cursor = 0
		if prevPath != "" {
			for i, r := range m.rows {
				if r.Path == prevPath {
					m.cursor = i
					break
				}
			}
		}
		m.refreshView()
		// Async-refetch hunks for files that are still expanded so their
		// diffs reflect the new working tree. Old hunks remain visible until
		// the new ones arrive, so there's no "loading" flicker.
		var cmds []tea.Cmd
		var walk func(n *tree.Node)
		walk = func(n *tree.Node) {
			if !n.IsDir && n.Expanded && n.File != nil && !n.File.Binary {
				cmds = append(cmds, loadHunksCmd(m.repoRoot, n.Path, *n.File, ""))
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(m.root)
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case logMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.commits = msg.commits
		m.logLoaded = true
		if m.commitCursor >= len(m.commits) {
			m.commitCursor = 0
		}
		m.refreshView()
		return m, nil

	case commitTreeMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Ignore stale responses (user already navigated away).
		if msg.sha != m.selectedSHA {
			return m, nil
		}
		m.err = nil
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
	case key.Matches(msg, keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return *m, m.refreshCmd()
	case key.Matches(msg, keys.ToggleAll):
		return *m, m.cycleMode()
	case key.Matches(msg, keys.Back):
		if m.mode == ModeCommit {
			return *m, m.exitCommit()
		}
		return *m, nil
	}

	if m.mode == ModeLog {
		return m.handleKeyLog(msg)
	}
	return m.handleKeyTree(msg)
}

func (m *Model) handleKeyTree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
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

func (m *Model) handleKeyLog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Down):
		m.moveCommitCursor(1)
	case key.Matches(msg, keys.Up):
		m.moveCommitCursor(-1)
	case key.Matches(msg, keys.PgDn):
		m.moveCommitCursor(m.vp.Height / 2)
	case key.Matches(msg, keys.PgUp):
		m.moveCommitCursor(-m.vp.Height / 2)
	case key.Matches(msg, keys.Top):
		m.commitCursor = 0
		m.refreshView()
	case key.Matches(msg, keys.Bottom):
		m.commitCursor = len(m.commits) - 1
		m.refreshView()
	case key.Matches(msg, keys.Toggle), key.Matches(msg, keys.Right):
		return *m, m.openCommit()
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
	if m.mode == ModeLog {
		for i := range m.commits {
			if z := m.zones.Get(commitZoneID(i)); z.InBounds(msg) {
				m.commitCursor = i
				return *m, m.openCommit()
			}
		}
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

// --- mode + commit navigation ---

// resetTree clears tree-mode view state. Called when switching into a mode
// that renders something other than the working-tree diff.
func (m *Model) resetTree() {
	m.root = &tree.Node{IsDir: true, Expanded: true}
	m.rows = nil
	m.cursor = 0
}

func (m *Model) clearSelectedCommit() {
	m.selectedSHA = ""
	m.selectedShort = ""
	m.selectedSubject = ""
}

func (m *Model) cycleMode() tea.Cmd {
	m.vp.SetYOffset(0)
	switch m.mode {
	case ModeChanged:
		m.mode = ModeAll
		return loadStatusCmd(m.repoRoot, true)
	case ModeAll:
		m.mode = ModeLog
		m.resetTree()
		if !m.logLoaded {
			return loadLogCmd(m.repoRoot)
		}
		m.refreshView()
		return nil
	case ModeLog, ModeCommit:
		m.mode = ModeChanged
		m.clearSelectedCommit()
		return loadStatusCmd(m.repoRoot, false)
	}
	return nil
}

func (m *Model) openCommit() tea.Cmd {
	if m.commitCursor < 0 || m.commitCursor >= len(m.commits) {
		return nil
	}
	c := m.commits[m.commitCursor]
	m.mode = ModeCommit
	m.selectedSHA = c.SHA
	m.selectedShort = c.ShortSHA
	m.selectedSubject = c.Subject
	m.resetTree()
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadCommitTreeCmd(m.repoRoot, c.SHA)
}

func (m *Model) exitCommit() tea.Cmd {
	m.mode = ModeLog
	m.clearSelectedCommit()
	m.vp.SetYOffset(0)
	m.refreshView()
	return nil
}

func (m *Model) refreshCmd() tea.Cmd {
	switch m.mode {
	case ModeChanged:
		return loadStatusCmd(m.repoRoot, false)
	case ModeAll:
		return loadStatusCmd(m.repoRoot, true)
	case ModeLog:
		return loadLogCmd(m.repoRoot)
	case ModeCommit:
		if m.selectedSHA != "" {
			return loadCommitTreeCmd(m.repoRoot, m.selectedSHA)
		}
	}
	return nil
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
	if n.File == nil {
		return nil
	}
	if n.Expanded {
		n.Expanded = false
		n.Hunks = nil
		n.LoadErr = nil
		m.refreshView()
		return nil
	}
	n.Expanded = true
	if n.File.Binary {
		m.refreshView()
		return nil
	}
	n.Loading = true
	m.refreshView()
	return loadHunksCmd(m.repoRoot, n.Path, *n.File, m.selectedSHA)
}

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

func (m *Model) moveCommitCursor(d int) {
	if len(m.commits) == 0 {
		return
	}
	m.commitCursor += d
	if m.commitCursor < 0 {
		m.commitCursor = 0
	}
	if m.commitCursor >= len(m.commits) {
		m.commitCursor = len(m.commits) - 1
	}
	m.refreshView()
}

func (m *Model) preserveStateInto(newRoot *tree.Node) {
	if m.root == nil {
		return
	}
	type snap struct {
		expanded bool
		hunks    []git.Hunk
	}
	snaps := map[string]snap{}
	var collect func(n *tree.Node)
	collect = func(n *tree.Node) {
		if n.Path != "" {
			snaps[n.Path] = snap{expanded: n.Expanded, hunks: n.Hunks}
		}
		for _, c := range n.Children {
			collect(c)
		}
	}
	var apply func(n *tree.Node)
	apply = func(n *tree.Node) {
		if s, ok := snaps[n.Path]; ok && n.Path != "" {
			n.Expanded = s.expanded
			// Carry old hunks so the expanded view stays visible during the
			// brief gap until refreshed hunks arrive (avoids a "no diff"
			// flicker on every watcher-triggered reload).
			n.Hunks = s.hunks
		}
		for _, c := range n.Children {
			apply(c)
		}
	}
	collect(m.root)
	apply(newRoot)
}

func (m *Model) refreshView() {
	if !m.ready {
		return
	}
	if m.mode == ModeLog {
		if m.commitCursor >= len(m.commits) {
			m.commitCursor = len(m.commits) - 1
		}
		if m.commitCursor < 0 {
			m.commitCursor = 0
		}
		m.vp.SetContent(m.renderLog())
		m.ensureCommitCursorVisible()
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

func (m *Model) ensureCommitCursorVisible() {
	y := m.commitCursor
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
		c++
		c += len(h.Lines)
		for _, l := range h.Lines {
			if l.NoNewlineHere {
				c++
			}
		}
	}
	if c == 0 {
		c = 1
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
	if m.mode == ModeCommit && m.selectedShort != "" {
		crumb := dimStyle.Render(m.selectedShort + " " + truncate(m.selectedSubject, max(20, m.width-20)))
		return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", crumb)
	}
	repo := dimStyle.Render(m.repoRoot)
	return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", repo)
}

func (m Model) footer() string {
	if m.showHelp {
		return helpStyle.Render("j/k move · enter open/toggle · esc back · a changed/all/log · r refresh · q quit")
	}
	if m.err != nil {
		return errStyle.Render("error: " + m.err.Error())
	}
	if m.mode == ModeLog {
		left := fmt.Sprintf("[log] %d commits", len(m.commits))
		return helpStyle.Render(left + " · enter open · a toggle · ? help")
	}

	// ModeChanged / ModeAll / ModeCommit all show file count + adds/dels totals.
	files := 0
	for _, n := range m.rows {
		if !n.IsDir && n.File != nil {
			files++
		}
	}
	adds, dels := 0, 0
	if m.root != nil {
		adds, dels = m.root.Adds, m.root.Dels
	}
	totals := addsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", dels))

	var left, hint string
	if m.mode == ModeCommit {
		left = fmt.Sprintf("[commit %s] %d files", m.selectedShort, files)
		hint = " · esc back · ? help"
	} else {
		left = fmt.Sprintf("[%s] %d changed", m.mode.String(), files)
		hint = " · a toggle · ? help"
	}
	return helpStyle.Render(left+" · ") + totals + helpStyle.Render(hint)
}

func (m *Model) renderBody() string {
	if len(m.rows) == 0 {
		if m.err != nil {
			return errStyle.Render(m.err.Error())
		}
		if m.mode == ModeCommit {
			return dimStyle.Render("  loading commit…")
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

func (m *Model) renderLog() string {
	if len(m.commits) == 0 {
		if m.err != nil {
			return errStyle.Render(m.err.Error())
		}
		if m.logLoaded {
			return dimStyle.Render("  no commits")
		}
		return dimStyle.Render("  loading log…")
	}
	var b strings.Builder
	for i, c := range m.commits {
		row := m.renderCommitRow(i, c)
		b.WriteString(m.zones.Mark(commitZoneID(i), row))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderCommitRow(i int, c git.Commit) string {
	sha := addsStyle.Render(c.ShortSHA)
	date := dimStyle.Render(c.Date)
	author := dimStyle.Render(truncate(c.Author, 14))
	subj := fileStyle.Render(c.Subject)
	row := fmt.Sprintf("%s  %s  %s  %s", sha, date, author, subj)
	return m.applyCursor(row, i == m.commitCursor)
}

// applyCursor pads `row` to full viewport width and applies cursorStyle when
// `selected` is true; otherwise returns row unchanged.
func (m *Model) applyCursor(row string, selected bool) string {
	if !selected {
		return row
	}
	if pad := m.width - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return cursorStyle.Render(row)
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
		name = "  " + dimStyle.Render(n.Name)
	}
	counts := ""
	if n.Adds > 0 || n.Dels > 0 {
		counts = " " + addsStyle.Render(fmt.Sprintf("+%d", n.Adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", n.Dels))
	}
	row := fmt.Sprintf("%s%s %s", indent, chev, name) + counts
	return m.applyCursor(row, i == m.cursor)
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

func zoneID(i int) string       { return fmt.Sprintf("row-%d", i) }
func commitZoneID(i int) string { return fmt.Sprintf("commit-%d", i) }

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return s[:w-1] + "…"
}
