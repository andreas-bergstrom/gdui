package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/andreasbergstrom/gd/internal/git"
	"github.com/andreasbergstrom/gd/internal/render"
	"github.com/andreasbergstrom/gd/internal/tree"
)

const (
	logLimit = 100
	// scrollOff keeps a margin of context lines around the cursor when
	// auto-scrolling, so the user can see what they're navigating toward
	// instead of the cursor sitting flush against the screen edge.
	scrollOff = 4
)

type viewMode int

const (
	ModeChanged viewMode = iota
	ModeAll
	ModeLog
	ModeCommit
	ModeFileLog
	ModeSearch
)

func (m viewMode) String() string {
	switch m {
	case ModeAll:
		return "all"
	case ModeLog:
		return "log"
	case ModeCommit:
		return "commit"
	case ModeFileLog:
		return "file log"
	case ModeSearch:
		return "search"
	default:
		return "changed"
	}
}

type Model struct {
	repoRoot string
	mode     viewMode

	// tree state — used in Changed/All/Commit modes
	root       *tree.Node
	rows       []*tree.Node
	cursor     int
	diffCursor int // line index inside the expanded file at `cursor`; -1 = on the file row itself

	// log state — used in Log mode
	commits      []git.Commit
	commitCursor int
	logLoaded    bool // true once a logMsg has been processed (distinguishes "loading" from "empty repo")

	// commit drill-in state — set when mode == ModeCommit
	selectedSHA     string
	selectedShort   string
	selectedSubject string

	// file-log state — set when mode == ModeFileLog
	fileLogPath  string
	prevTreeMode viewMode // ModeChanged or ModeAll, to return to from ModeFileLog
	commitParent viewMode // ModeLog or ModeFileLog, to return to from ModeCommit

	vp       viewport.Model
	width    int
	height   int
	err      error
	zones    *zone.Manager
	showHelp bool
	ready    bool

	// search holds everything specific to ModeSearch (input, cached path
	// index, current results, cursor, transient toast). See search.go.
	search searchState
}

func New(repoRoot string) Model {
	return Model{
		repoRoot:   repoRoot,
		mode:       ModeChanged,
		root:       &tree.Node{IsDir: true, Expanded: true},
		zones:      zone.New(),
		diffCursor: -1,
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

func loadFileLogCmd(root, path string) tea.Cmd {
	return func() tea.Msg {
		c, err := git.LogForPath(root, path, logLimit)
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
		if !m.ready {
			m.vp = viewport.New(msg.Width, 1)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
		}
		m.recomputeViewportHeight()
		m.refreshView()
		return m, nil

	case RefreshMsg:
		// File-watcher triggered refresh. The cached path index used by
		// ModeSearch is potentially stale once files appear/disappear; mark
		// it for reload regardless of the current mode.
		m.search.pathsReady = false
		m.search.paths = nil
		switch m.mode {
		case ModeChanged, ModeAll:
			return m, loadStatusCmd(m.repoRoot, m.mode == ModeAll)
		case ModeLog:
			return m, loadLogCmd(m.repoRoot)
		case ModeFileLog:
			if m.fileLogPath != "" {
				return m, loadFileLogCmd(m.repoRoot, m.fileLogPath)
			}
		case ModeSearch:
			return m, loadSearchPathsCmd(m.repoRoot)
		}
		// ModeCommit: a single commit's contents are immutable; nothing to do.
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
		prevDiffCursor := m.diffCursor
		m.cursor = 0
		m.diffCursor = -1
		if prevPath != "" {
			for i, r := range m.rows {
				if r.Path == prevPath {
					m.cursor = i
					m.diffCursor = prevDiffCursor // keep in-diff position on the same file
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
		// Preserve the cursor's SHA across reloads so a watcher-triggered
		// refresh (new commit) doesn't yank the cursor off the user's row.
		var prevSHA string
		if m.commitCursor >= 0 && m.commitCursor < len(m.commits) {
			prevSHA = m.commits[m.commitCursor].SHA
		}
		m.commits = msg.commits
		m.logLoaded = true
		m.commitCursor = 0
		if prevSHA != "" {
			for i, c := range m.commits {
				if c.SHA == prevSHA {
					m.commitCursor = i
					break
				}
			}
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
			// Capture the cursor's y BEFORE the hunk count changes so we can
			// shift YOffset by the same delta — otherwise inserting N diff
			// lines above the cursor pushes it visually off-screen.
			prevY := m.cursorY()
			n.Loading = false
			n.LoadErr = msg.err
			n.Hunks = msg.hunks
			m.refreshView()
			if delta := m.cursorY() - prevY; delta != 0 {
				m.vp.SetYOffset(m.vp.YOffset + delta)
			}
		}
		return m, nil

	case searchPathsMsg:
		m.search.pathsReady = true
		m.search.pathsErr = msg.err
		if msg.err != nil {
			m.refreshView()
			return m, nil
		}
		m.search.paths = msg.paths
		m.refreshView()
		if m.mode == ModeSearch {
			return m, m.kickSearch()
		}
		return m, nil

	case searchResultMsg:
		// Drop stale results — a newer query may already be in-flight.
		if msg.seq != m.search.pendingQ {
			return m, nil
		}
		m.search.result = msg.result
		m.search.resultSeq = msg.seq
		if m.search.cursor >= m.searchTotal() {
			m.search.cursor = m.searchTotal() - 1
			if m.search.cursor < 0 {
				m.search.cursor = 0
			}
		}
		m.refreshView()
		return m, nil

	case clipboardCopiedMsg:
		if msg.err != nil && msg.seq == m.search.toastSeq {
			m.search.toast = "copy failed: " + msg.err.Error()
			m.search.toastUntil = time.Now().Add(toastDuration)
			m.refreshView()
		}
		return m, nil

	case clipboardToastExpiredMsg:
		// Only clear the toast if no newer copy has happened since.
		if msg.seq == m.search.toastSeq {
			m.search.toast = ""
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
	// Search mode owns its own input (printable runes go into the query),
	// so dispatch to it before consulting the global bindings — the user
	// must be able to type 'q', 'r', '/', 'a', etc. without triggering them.
	if m.mode == ModeSearch {
		// Allow ctrl+c to still quit and ? to still toggle help.
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if msg.String() == "?" {
			m.showHelp = !m.showHelp
			return *m, nil
		}
		return m.handleKeySearch(msg)
	}

	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return *m, m.refreshCmd()
	case key.Matches(msg, keys.Search):
		return *m, m.enterSearch()
	case key.Matches(msg, keys.ToggleAll):
		return *m, m.cycleMode()
	case key.Matches(msg, keys.Back):
		if m.mode == ModeCommit {
			return *m, m.exitCommit()
		}
		if m.mode == ModeFileLog {
			return *m, m.exitFileLog()
		}
		return *m, nil
	}

	if m.mode == ModeLog || m.mode == ModeFileLog {
		return m.handleKeyLog(msg)
	}
	return m.handleKeyTree(msg)
}

func (m *Model) handleKeyTree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Blame):
		n := m.currentNode()
		if n == nil || n.IsDir || n.File == nil {
			return *m, nil
		}
		return *m, m.openFileLog(n.Path)
	case key.Matches(msg, keys.Down):
		m.moveDown()
	case key.Matches(msg, keys.Up):
		m.moveUp()
	case key.Matches(msg, keys.PgDn):
		m.diffCursor = -1
		m.moveCursor(m.vp.Height / 2)
	case key.Matches(msg, keys.PgUp):
		m.diffCursor = -1
		m.moveCursor(-m.vp.Height / 2)
	case key.Matches(msg, keys.NextDir):
		m.diffCursor = -1
		m.jumpDir(1)
	case key.Matches(msg, keys.PrevDir):
		m.diffCursor = -1
		m.jumpDir(-1)
	case key.Matches(msg, keys.Top):
		m.cursor = 0
		m.diffCursor = -1
		m.refreshViewToCursor()
	case key.Matches(msg, keys.Bottom):
		m.cursor = len(m.rows) - 1
		m.diffCursor = -1
		m.refreshViewToCursor()
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
		// In-diff cursor or expanded file → collapse.
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
			m.diffCursor = -1
			m.refreshViewToCursor()
		}
	}
	return *m, nil
}

// moveDown advances one step. If the cursor is on an expanded file with
// rendered diff content, it walks through the diff lines first; once past the
// last diff line, it moves to the next tree row.
func (m *Model) moveDown() {
	n := m.currentNode()
	if n != nil && !n.IsDir && n.Expanded {
		if max := diffNavCount(n); max > 0 {
			if m.diffCursor < max-1 {
				m.diffCursor++
				m.refreshViewToCursor()
				return
			}
		}
	}
	m.diffCursor = -1
	m.moveCursor(1)
}

// moveUp is the inverse of moveDown.
func (m *Model) moveUp() {
	if m.diffCursor > 0 {
		m.diffCursor--
		m.refreshViewToCursor()
		return
	}
	if m.diffCursor == 0 {
		m.diffCursor = -1
		m.refreshViewToCursor()
		return
	}
	m.moveCursor(-1)
}

// diffNavCount is the number of diff lines navigable inside an expanded file.
// Returns 0 when there's nothing to navigate (loading, binary, error, no diff).
func diffNavCount(n *tree.Node) int {
	if n == nil || n.IsDir || n.File == nil {
		return 0
	}
	if n.File.Binary || n.Loading || n.LoadErr != nil {
		return 0
	}
	return render.HunkLineCount(n.Hunks)
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
		m.refreshViewToCursor()
	case key.Matches(msg, keys.Bottom):
		m.commitCursor = len(m.commits) - 1
		m.refreshViewToCursor()
	case key.Matches(msg, keys.Toggle), key.Matches(msg, keys.Right):
		return *m, m.openCommit()
	}
	return *m, nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == ModeSearch {
		return m.handleSearchMouse(msg)
	}
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
	if m.mode == ModeLog || m.mode == ModeFileLog {
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
			m.diffCursor = -1
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
	m.diffCursor = -1
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
	case ModeLog, ModeCommit, ModeFileLog:
		m.mode = ModeChanged
		m.clearSelectedCommit()
		m.fileLogPath = ""
		return loadStatusCmd(m.repoRoot, false)
	}
	return nil
}

func (m *Model) openCommit() tea.Cmd {
	if m.commitCursor < 0 || m.commitCursor >= len(m.commits) {
		return nil
	}
	c := m.commits[m.commitCursor]
	m.commitParent = m.mode
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
	parent := m.commitParent
	if parent != ModeLog && parent != ModeFileLog {
		parent = ModeLog
	}
	m.mode = parent
	m.clearSelectedCommit()
	m.vp.SetYOffset(0)
	m.refreshView()
	return nil
}

func (m *Model) openFileLog(path string) tea.Cmd {
	m.prevTreeMode = m.mode
	m.mode = ModeFileLog
	m.fileLogPath = path
	m.resetTree()
	m.commits = nil
	m.commitCursor = 0
	m.logLoaded = false
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadFileLogCmd(m.repoRoot, path)
}

func (m *Model) exitFileLog() tea.Cmd {
	prev := m.prevTreeMode
	if prev != ModeChanged && prev != ModeAll {
		prev = ModeChanged
	}
	m.mode = prev
	m.fileLogPath = ""
	m.commits = nil
	m.logLoaded = false
	m.vp.SetYOffset(0)
	return loadStatusCmd(m.repoRoot, prev == ModeAll)
}

func (m *Model) refreshCmd() tea.Cmd {
	switch m.mode {
	case ModeChanged:
		return loadStatusCmd(m.repoRoot, false)
	case ModeAll:
		return loadStatusCmd(m.repoRoot, true)
	case ModeLog:
		return loadLogCmd(m.repoRoot)
	case ModeFileLog:
		if m.fileLogPath != "" {
			return loadFileLogCmd(m.repoRoot, m.fileLogPath)
		}
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
	m.diffCursor = -1
	if n.IsDir {
		n.Expanded = !n.Expanded
		m.refreshViewToCursor()
		return nil
	}
	if n.File == nil {
		return nil
	}
	if n.Expanded {
		n.Expanded = false
		n.Hunks = nil
		n.LoadErr = nil
		m.refreshViewToCursor()
		return nil
	}
	n.Expanded = true
	if n.File.Binary {
		m.refreshViewToCursor()
		return nil
	}
	n.Loading = true
	m.refreshViewToCursor()
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
			m.refreshViewToCursor()
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
	m.refreshViewToCursor()
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
	m.refreshViewToCursor()
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

// recomputeViewportHeight resizes the viewport based on the actual rendered
// height of the header and footer. In a narrow sidebar the footer string can
// soft-wrap to 2+ lines; if the viewport keeps assuming 1, the body overflows
// the terminal and the bottom rows (where the cursor sits) get clipped — making
// the cursor look like it's drifted off-screen even though the math says it's
// visible.
func (m *Model) recomputeViewportHeight() {
	if !m.ready {
		return
	}
	headerH := lipgloss.Height(m.header())
	footerH := lipgloss.Height(m.footer())
	vpH := m.height - headerH - footerH
	if vpH < 1 {
		vpH = 1
	}
	m.vp.Height = vpH
}

// refreshView re-renders the viewport content without touching the scroll
// position. Use this for content changes (status reload, hunk arrival) so that
// a watcher-triggered refresh doesn't yank the viewport back to the cursor row
// while the user is reading further down inside an expanded diff.
func (m *Model) refreshView() {
	if !m.ready {
		return
	}
	m.recomputeViewportHeight()
	if m.mode == ModeSearch {
		m.vp.SetContent(m.renderSearch())
		return
	}
	if m.mode == ModeLog || m.mode == ModeFileLog {
		if m.commitCursor >= len(m.commits) {
			m.commitCursor = len(m.commits) - 1
		}
		if m.commitCursor < 0 {
			m.commitCursor = 0
		}
		m.vp.SetContent(m.renderLog())
		return
	}
	m.rows = tree.Flatten(m.root)
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.diffCursor >= 0 {
		if max := diffNavCount(m.currentNode()); max == 0 {
			m.diffCursor = -1
		} else if m.diffCursor >= max {
			m.diffCursor = max - 1
		}
	}
	m.vp.SetContent(m.renderBody())
}

// refreshViewToCursor rebuilds and then scrolls the cursor into view. Use this
// from explicit cursor-moving handlers (key navigation, click, expand/collapse).
func (m *Model) refreshViewToCursor() {
	m.refreshView()
	if !m.ready {
		return
	}
	if m.mode == ModeLog || m.mode == ModeFileLog {
		m.ensureCommitCursorVisible()
		return
	}
	m.ensureCursorVisible()
}

func (m *Model) ensureCursorVisible() {
	m.scrollTo(m.cursorY())
}

// cursorY returns the line index (within the rendered body) of the cursor —
// either the cursor's tree row or, when stepping inside an expanded file, the
// highlighted diff line.
func (m *Model) cursorY() int {
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
	if m.diffCursor >= 0 {
		y += 1 + m.diffCursor
	}
	return y
}

func (m *Model) ensureCommitCursorVisible() {
	m.scrollTo(m.commitCursor)
}

// scrollTo adjusts the viewport so line `y` (0-indexed within the rendered
// content) is visible with a scrollOff-line margin from each edge. The margin
// shrinks gracefully on tiny viewports.
func (m *Model) scrollTo(y int) {
	margin := scrollOff
	if m.vp.Height <= 2*scrollOff+1 {
		margin = m.vp.Height / 2
	}
	if y < m.vp.YOffset+margin {
		m.vp.SetYOffset(y - margin)
	} else if y >= m.vp.YOffset+m.vp.Height-margin {
		m.vp.SetYOffset(y - m.vp.Height + 1 + margin)
	}
}

// hunkLineCount returns the number of body lines an expanded file row will
// occupy. For "real" diff content this is render.HunkLineCount; for placeholder
// rows (binary, loading, error, empty) we always render one line.
func hunkLineCount(n *tree.Node) int {
	if n.File != nil && n.File.Binary {
		return 1
	}
	if n.Loading || n.LoadErr != nil {
		return 1
	}
	c := render.HunkLineCount(n.Hunks)
	if c == 0 {
		return 1 // ⟨no diff⟩ placeholder
	}
	return c
}

// --- view ---

func (m Model) View() string {
	if !m.ready {
		return ""
	}
	header := m.fitWidth(m.header())
	footer := m.fitWidth(m.footer())
	var body string
	if m.showHelp {
		body = m.renderHelpBody()
	} else {
		body = m.vp.View()
	}
	return m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, header, body, footer))
}

func (m Model) header() string {
	title := headerStyle.Render(" gd ")
	if m.mode == ModeCommit && m.selectedShort != "" {
		crumb := dimStyle.Render(m.selectedShort + " " + truncate(m.selectedSubject, max(20, m.width-20)))
		return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", crumb)
	}
	if m.mode == ModeFileLog && m.fileLogPath != "" {
		crumb := dimStyle.Render("log: " + m.fileLogPath)
		return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", crumb)
	}
	if m.mode == ModeSearch {
		crumb := dimStyle.Render("search")
		return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", crumb)
	}
	repo := dimStyle.Render(m.repoRoot)
	return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", repo)
}

func (m Model) footer() string {
	if m.showHelp {
		return helpStyle.Render("? close help · q quit")
	}
	if m.mode == ModeSearch {
		return m.searchFooter()
	}
	if m.err != nil {
		return errStyle.Render("error: " + m.err.Error())
	}
	if m.mode == ModeLog {
		left := fmt.Sprintf("[log] %d commits", len(m.commits))
		return helpStyle.Render(left + " · enter open · a toggle · ? help")
	}
	if m.mode == ModeFileLog {
		left := fmt.Sprintf("[file log] %d commits · %s", len(m.commits), m.fileLogPath)
		return helpStyle.Render(left + " · enter open · esc back · ? help")
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
		row := m.fitWidth(m.renderRow(i, n))
		b.WriteString(m.zones.Mark(zoneID(i), row))
		b.WriteByte('\n')
		if !n.IsDir && n.Expanded {
			b.WriteString(m.fitLines(m.renderExpanded(n)))
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
		row := m.fitWidth(m.renderCommitRow(i, c))
		b.WriteString(m.zones.Mark(commitZoneID(i), row))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// fitWidth truncates a single line to the viewport width if it would otherwise
// soft-wrap inside the viewport (lipgloss wraps lines wider than its style
// Width, which silently inflates rendered line count and breaks cursor-y math).
func (m *Model) fitWidth(line string) string {
	if m.width <= 0 || lipgloss.Width(line) <= m.width {
		return line
	}
	return render.TruncateANSI(line, m.width)
}

// fitLines applies fitWidth to every line in a multi-line block.
func (m *Model) fitLines(block string) string {
	if m.width <= 0 {
		return block
	}
	lines := strings.Split(block, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) > m.width {
			lines[i] = render.TruncateANSI(ln, m.width)
		}
	}
	return strings.Join(lines, "\n")
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
	cursor := -1
	if i := m.cursor; i >= 0 && i < len(m.rows) && m.rows[i] == n {
		cursor = m.diffCursor
	}
	return render.Hunks(n.Path, n.Hunks, m.width, cursor)
}

func (m *Model) renderHelpBody() string {
	type row struct{ key, desc string }
	sections := []struct {
		title string
		rows  []row
	}{
		{"Navigation", []row{
			{"j / ↓", "move cursor down"},
			{"k / ↑", "move cursor up"},
			{"h / ←", "collapse (or jump to parent)"},
			{"l / →", "expand"},
			{"[ / ]", "previous / next folder"},
			{"g / G", "top / bottom"},
			{"ctrl+u / ctrl+d", "page up / down"},
		}},
		{"Open & inspect", []row{
			{"enter / space", "toggle expand/collapse (or open commit)"},
			{"b", "file history (commits touching this file)"},
			{"left-click row", "toggle expand/collapse"},
			{"scroll wheel", "scroll viewport"},
		}},
		{"Search", []row{
			{"/", "open global search"},
			{"*.go", "glob query — match filenames only"},
			{"enter", "(in search) copy path or path:line"},
			{"ctrl+u", "(in search) clear query"},
			{"esc", "(in search) close"},
		}},
		{"Modes", []row{
			{"a", "cycle view: changed → all → log"},
			{"esc / backspace", "back out of commit or file history"},
		}},
		{"Misc", []row{
			{"r", "refresh manually"},
			{"?", "toggle this help"},
			{"q / ctrl+c", "quit"},
		}},
	}

	keyW := 0
	for _, s := range sections {
		for _, r := range s.rows {
			if len(r.key) > keyW {
				keyW = len(r.key)
			}
		}
	}

	keyStyle := headerStyle
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#bb9af7")).Bold(true)

	var b strings.Builder
	b.WriteString(titleStyle.Render("  gd — keys"))
	b.WriteString("\n")
	for _, s := range sections {
		b.WriteString("\n  ")
		b.WriteString(titleStyle.Render(s.title))
		b.WriteString("\n")
		for _, r := range s.rows {
			padded := r.key + strings.Repeat(" ", keyW-len(r.key))
			line := "    " + keyStyle.Render(padded) + "  " + fileStyle.Render(r.desc)
			b.WriteString(m.fitWidth(line))
			b.WriteByte('\n')
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	if m.vp.Height > 0 {
		lines := strings.Count(out, "\n") + 1
		if pad := m.vp.Height - lines; pad > 0 {
			out += strings.Repeat("\n", pad)
		}
	}
	return out
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
