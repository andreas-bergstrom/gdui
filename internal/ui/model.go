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

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/render"
	"github.com/andreas-bergstrom/gdui/internal/tree"
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
	// repoRoot is the worktree gdui was launched in. Used to identify the
	// "active" section initially and as a fallback root for git operations
	// before sections have loaded.
	repoRoot string
	mode     viewMode

	// sections drive ModeChanged/ModeAll rendering. In ModeLog/ModeCommit/
	// ModeFileLog, sections only determine which worktree's data is shown
	// via activeWT.
	sections []*WorktreeSection
	activeWT int

	// rows is the unified flat row list rendered in the current mode.
	rows       []displayRow
	cursor     int
	diffCursor int // line index inside the expanded file at `cursor`; -1 = on the file row itself

	// commitTree is the tree for the currently-selected commit (ModeCommit).
	// Independent of sections — a commit belongs to exactly one worktree at a
	// time and survives section reorders.
	commitTree *tree.Node

	// log state — used in Log / FileLog modes; always belongs to the active
	// section's worktree.
	commits      []git.Commit
	commitCursor int
	logLoaded    bool
	logRoot      string // worktree the m.commits belong to (for stale-msg drop)

	// commit drill-in state — set when mode == ModeCommit
	selectedSHA     string
	selectedShort   string
	selectedSubject string
	selectedRoot    string // worktree the selected commit belongs to

	// file-log state — set when mode == ModeFileLog
	fileLogPath  string
	fileLogRoot  string // section root that owns fileLogPath
	prevTreeMode viewMode
	commitParent viewMode

	vp       viewport.Model
	width    int
	height   int
	err      error
	zones    *zone.Manager
	showHelp bool
	ready    bool

	// initialized flips to true after the first initDataMsg so we know
	// whether to auto-position the cursor on the launch-cwd's section.
	initialized bool

	search searchState
}

func New(repoRoot string) Model {
	return Model{
		repoRoot:   repoRoot,
		mode:       ModeChanged,
		zones:      zone.New(),
		diffCursor: -1,
	}
}

func (m Model) Init() tea.Cmd {
	return loadInitDataCmd(m.repoRoot, false)
}

// --- messages ---

// RefreshMsg can be sent externally (e.g. from a file watcher) to trigger a
// per-worktree status reload. An empty Root means "manual refresh — re-list
// worktrees and reload everything." Ignored in Log/FileLog/Commit modes
// (those views aren't tied to working-tree state).
type RefreshMsg struct {
	Root string
}

// initDataMsg carries one synchronous batch of worktree discovery + per-section
// status loads. Used at startup and on full manual refresh, where loading
// everything in one cmd avoids the test/runtime complexity of tea.Batch fan-out.
type initDataMsg struct {
	worktrees []git.Worktree
	statuses  map[string]sectionStatus
	allMode   bool
	err       error
}

type sectionStatus struct {
	files []git.ChangedFile
	tree  *tree.Node
	err   error
}

type statusMsg struct {
	root  string
	files []git.ChangedFile
	tree  *tree.Node
	err   error
}

type logMsg struct {
	root    string
	commits []git.Commit
	err     error
}

type commitTreeMsg struct {
	root string
	sha  string
	tree *tree.Node
	err  error
}

type hunksMsg struct {
	root  string
	path  string
	hunks []git.Hunk
	err   error
}

func loadInitDataCmd(repoRoot string, allMode bool) tea.Cmd {
	return func() tea.Msg {
		wts, err := git.ListWorktrees(repoRoot)
		if err != nil {
			return initDataMsg{err: err, allMode: allMode}
		}
		statuses := map[string]sectionStatus{}
		for _, wt := range wts {
			statuses[wt.Root] = loadSectionStatus(wt.Root, allMode)
		}
		return initDataMsg{worktrees: wts, statuses: statuses, allMode: allMode}
	}
}

func loadStatusCmd(root string, allMode bool) tea.Cmd {
	return func() tea.Msg {
		st := loadSectionStatus(root, allMode)
		return statusMsg{root: root, files: st.files, tree: st.tree, err: st.err}
	}
}

func loadSectionStatus(root string, allMode bool) sectionStatus {
	files, err := git.Status(root)
	if err != nil {
		return sectionStatus{err: err}
	}
	if !allMode {
		return sectionStatus{files: files, tree: tree.Build(files)}
	}
	all, err := git.ListAll(root)
	if err != nil {
		return sectionStatus{files: files, err: err}
	}
	return sectionStatus{files: files, tree: tree.BuildAll(files, all)}
}

func loadLogCmd(root string) tea.Cmd {
	return func() tea.Msg {
		c, err := git.Log(root, logLimit)
		return logMsg{root: root, commits: c, err: err}
	}
}

func loadFileLogCmd(root, path string) tea.Cmd {
	return func() tea.Msg {
		c, err := git.LogForPath(root, path, logLimit)
		return logMsg{root: root, commits: c, err: err}
	}
}

func loadCommitTreeCmd(root, sha string) tea.Cmd {
	return func() tea.Msg {
		files, err := git.CommitFiles(root, sha)
		if err != nil {
			return commitTreeMsg{root: root, sha: sha, err: err}
		}
		return commitTreeMsg{root: root, sha: sha, tree: tree.Build(files)}
	}
}

func loadHunksCmd(root, path string, file git.ChangedFile, sha string) tea.Cmd {
	return func() tea.Msg {
		var hs []git.Hunk
		var err error
		if sha == "" {
			hs, err = git.LoadHunks(root, file)
		} else {
			hs, err = git.CommitHunks(root, sha, file.Path)
		}
		return hunksMsg{root: root, path: path, hunks: hs, err: err}
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
		// File-watcher triggered. Cached search index is potentially stale.
		m.search.pathsReady = false
		m.search.paths = nil
		switch m.mode {
		case ModeChanged, ModeAll:
			if msg.Root == "" {
				return m, loadInitDataCmd(m.repoRoot, m.mode == ModeAll)
			}
			// Per-section refresh from a watcher; only reload the affected one.
			if findSectionByRoot(m.sections, msg.Root) < 0 {
				return m, nil
			}
			return m, loadStatusCmd(msg.Root, m.mode == ModeAll)
		case ModeLog:
			return m, loadLogCmd(m.activeRoot())
		case ModeFileLog:
			if m.fileLogPath != "" {
				return m, loadFileLogCmd(m.fileLogRoot, m.fileLogPath)
			}
		case ModeSearch:
			return m, loadSearchPathsCmd(m.repoRoot)
		}
		return m, nil

	case initDataMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		firstInit := !m.initialized
		m.initialized = true
		m.applyInitData(msg)
		m.refreshView()
		if firstInit {
			m.positionCursorOnActiveSection()
			m.ensureCursorVisible()
		}
		return m, m.refreshExpandedHunks()

	case statusMsg:
		idx := findSectionByRoot(m.sections, msg.root)
		if idx < 0 {
			return m, nil // section was removed mid-flight
		}
		s := m.sections[idx]
		if msg.err != nil {
			s.LoadErr = msg.err
			s.Files = nil
			s.Root = nil
			m.refreshView()
			return m, nil
		}
		// Preserve in-section expand state across reloads.
		preserveTreeState(s.Root, msg.tree)
		prevAnchor := m.cursorAnchor()
		s.Files = msg.files
		s.Root = msg.tree
		s.LoadErr = nil
		if !s.firstLoadDone {
			s.firstLoadDone = true
			if sectionHasChanges(s) || idx == m.activeWT {
				s.Expanded = true
			}
		}
		m.err = nil
		prevDiffCursor := m.diffCursor
		m.refreshView()
		m.restoreCursor(prevAnchor, prevDiffCursor)
		return m, m.refreshExpandedHunks()

	case logMsg:
		// Stale-message drop: a tab-switch may have changed the active section.
		if msg.root != m.activeRoot() {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		var prevSHA string
		if m.commitCursor >= 0 && m.commitCursor < len(m.commits) {
			prevSHA = m.commits[m.commitCursor].SHA
		}
		m.commits = msg.commits
		m.logLoaded = true
		m.logRoot = msg.root
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
		// Ignore stale responses (user navigated away).
		if msg.sha != m.selectedSHA || msg.root != m.selectedRoot {
			return m, nil
		}
		m.err = nil
		m.commitTree = msg.tree
		m.cursor = 0
		m.refreshView()
		return m, nil

	case hunksMsg:
		var n *tree.Node
		// In commit mode, look up in m.commitTree; otherwise look up in the
		// originating section.
		if m.mode == ModeCommit && msg.root == m.selectedRoot {
			n = tree.FindByPath(m.commitTree, msg.path)
		} else if idx := findSectionByRoot(m.sections, msg.root); idx >= 0 {
			n = tree.FindByPath(m.sections[idx].Root, msg.path)
		}
		if n != nil && !n.IsDir {
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

// applyInitData rebuilds the section list from a fresh worktree discovery,
// preserving per-section expand state and per-node expand/hunk state where
// the worktree root survives the refresh.
func (m *Model) applyInitData(msg initDataMsg) {
	prev := m.sections
	newSecs := make([]*WorktreeSection, 0, len(msg.worktrees))
	for _, wt := range msg.worktrees {
		st := msg.statuses[wt.Root]
		var prevSec *WorktreeSection
		if i := findSectionByRoot(prev, wt.Root); i >= 0 {
			prevSec = prev[i]
		}
		s := &WorktreeSection{WT: wt}
		if prevSec != nil {
			s.Expanded = prevSec.Expanded
			s.firstLoadDone = prevSec.firstLoadDone
			preserveTreeState(prevSec.Root, st.tree)
		} else {
			s.Expanded = wt.Root == m.repoRoot // active worktree opens by default
		}
		s.Files = st.files
		s.Root = st.tree
		s.LoadErr = st.err
		if !s.firstLoadDone {
			s.firstLoadDone = true
			if len(st.files) > 0 || wt.Root == m.repoRoot {
				s.Expanded = true
			}
		}
		newSecs = append(newSecs, s)
	}
	m.sections = newSecs
	// activeWT: pick the section matching launch root, else clamp.
	m.activeWT = 0
	for i, s := range m.sections {
		if s.WT.Root == m.repoRoot {
			m.activeWT = i
			break
		}
	}
}

// preserveTreeState copies expand state and cached hunks from old to new
// keyed by Path, so a refresh doesn't drop the user's open files or flicker.
func preserveTreeState(old, new *tree.Node) {
	if old == nil || new == nil {
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
	collect(old)
	var apply func(n *tree.Node)
	apply = func(n *tree.Node) {
		if s, ok := snaps[n.Path]; ok && n.Path != "" {
			n.Expanded = s.expanded
			n.Hunks = s.hunks
		}
		for _, c := range n.Children {
			apply(c)
		}
	}
	apply(new)
}

// refreshExpandedHunks kicks off async hunk reloads for every file that's
// currently expanded, across all sections (or the commit tree in commit mode).
func (m *Model) refreshExpandedHunks() tea.Cmd {
	var cmds []tea.Cmd
	walk := func(root, sha string, n *tree.Node) {
		var visit func(*tree.Node)
		visit = func(n *tree.Node) {
			if !n.IsDir && n.Expanded && n.File != nil && !n.File.Binary {
				cmds = append(cmds, loadHunksCmd(root, n.Path, *n.File, sha))
			}
			for _, c := range n.Children {
				visit(c)
			}
		}
		if n != nil {
			visit(n)
		}
	}
	if m.mode == ModeCommit && m.selectedRoot != "" && m.commitTree != nil {
		walk(m.selectedRoot, m.selectedSHA, m.commitTree)
	} else {
		for _, s := range m.sections {
			walk(s.WT.Root, "", s.Root)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// --- key handling ---

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == ModeSearch {
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
	prevRow := m.currentRow()
	switch {
	case key.Matches(msg, keys.Blame):
		n := m.currentTreeNode()
		if n == nil || n.IsDir || n.File == nil {
			return *m, nil
		}
		return *m, m.openFileLog(m.currentSectionRoot(), n.Path)
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
		m.jumpGroup(1)
	case key.Matches(msg, keys.PrevDir):
		m.diffCursor = -1
		m.jumpGroup(-1)
	case key.Matches(msg, keys.Top):
		m.cursor = 0
		m.diffCursor = -1
		m.refreshViewToCursor()
	case key.Matches(msg, keys.Bottom):
		m.cursor = len(m.rows) - 1
		m.diffCursor = -1
		m.refreshViewToCursor()
	case key.Matches(msg, keys.Toggle):
		return *m, m.toggle(m.currentRow())
	case key.Matches(msg, keys.Right):
		if r, ok := m.currentRow().(treeRow); ok && r.node != nil && !r.node.Expanded {
			return *m, m.toggle(r)
		}
		if r, ok := m.currentRow().(headerRow); ok && r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) && !m.sections[r.sectionIdx].Expanded {
			return *m, m.toggle(r)
		}
	case key.Matches(msg, keys.Left):
		switch r := m.currentRow().(type) {
		case treeRow:
			n := r.node
			if n == nil {
				return *m, nil
			}
			if n.Expanded {
				return *m, m.toggle(r)
			}
			if n.Parent != nil && n.Parent.Parent != nil {
				for i, dr := range m.rows {
					if t, ok := dr.(treeRow); ok && t.node == n.Parent {
						m.cursor = i
						break
					}
				}
				m.diffCursor = -1
				m.refreshViewToCursor()
			}
		case headerRow:
			if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) && m.sections[r.sectionIdx].Expanded {
				return *m, m.toggle(r)
			}
		}
	}
	// In multi-section views the section header line styles itself differently
	// from the tree rows around it (full-width bg vs. just a chevron+name).
	// bubbletea's partial-redraw drops paints on lines whose visible width
	// matches across frames but whose styled-byte structure differs — leaving
	// stale cursor highlight from the old position visible alongside the new
	// one. Force a full repaint when navigation crosses between a header row
	// and a tree row, or between sections. Same mitigation as toggle's
	// shrinking case in CLAUDE.md.
	if m.showSectionHeaders() && needsRepaintAfterMove(prevRow, m.currentRow()) {
		return *m, tea.ClearScreen
	}
	return *m, nil
}

// needsRepaintAfterMove returns true when a cursor move crosses a row-type
// boundary in a multi-section view: header↔tree or one section's rows to
// another section's. Same-section, same-type moves don't trigger the
// bubbletea redraw glitch, so we don't pay the flicker for them.
func needsRepaintAfterMove(prev, next displayRow) bool {
	if prev == nil || next == nil || prev == next {
		return false
	}
	prevSec, prevHeader := rowSectionAndKind(prev)
	nextSec, nextHeader := rowSectionAndKind(next)
	if prevSec != nextSec {
		return true
	}
	return prevHeader != nextHeader
}

func rowSectionAndKind(r displayRow) (sectionIdx int, isHeader bool) {
	switch row := r.(type) {
	case headerRow:
		return row.sectionIdx, true
	case treeRow:
		return row.sectionIdx, false
	}
	return -1, false
}

// moveDown advances one step. If the cursor is on an expanded file with diff
// content, walk through diff lines first; otherwise move to the next row.
func (m *Model) moveDown() {
	if n := m.currentTreeNode(); n != nil && !n.IsDir && n.Expanded {
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
	case key.Matches(msg, keys.NextWorktree):
		return *m, m.cycleActiveWorktree(1)
	case key.Matches(msg, keys.PrevWorktree):
		return *m, m.cycleActiveWorktree(-1)
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
	for i, r := range m.rows {
		switch row := r.(type) {
		case headerRow:
			if z := m.zones.Get(headerZoneID(row.sectionIdx)); z.InBounds(msg) {
				m.cursor = i
				m.diffCursor = -1
				return *m, m.toggle(row)
			}
		case treeRow:
			if row.node == nil {
				continue
			}
			if z := m.zones.Get(zoneID(i)); z.InBounds(msg) {
				m.cursor = i
				m.diffCursor = -1
				return *m, m.toggle(row)
			}
		}
	}
	return *m, nil
}

// --- mode + selection navigation ---

func (m *Model) cycleMode() tea.Cmd {
	m.vp.SetYOffset(0)
	switch m.mode {
	case ModeChanged:
		m.mode = ModeAll
		return m.reloadAllSections(true)
	case ModeAll:
		m.mode = ModeLog
		m.commitTree = nil
		m.cursor = 0
		m.diffCursor = -1
		if !m.logLoaded || m.logRoot != m.activeRoot() {
			return loadLogCmd(m.activeRoot())
		}
		m.refreshView()
		return nil
	case ModeLog, ModeCommit, ModeFileLog:
		m.mode = ModeChanged
		m.clearSelectedCommit()
		m.fileLogPath = ""
		m.fileLogRoot = ""
		return m.reloadAllSections(false)
	}
	return nil
}

func (m *Model) reloadAllSections(allMode bool) tea.Cmd {
	if len(m.sections) == 0 {
		return loadInitDataCmd(m.repoRoot, allMode)
	}
	cmds := make([]tea.Cmd, 0, len(m.sections))
	for _, s := range m.sections {
		cmds = append(cmds, loadStatusCmd(s.WT.Root, allMode))
	}
	return tea.Batch(cmds...)
}

func (m *Model) cycleActiveWorktree(d int) tea.Cmd {
	if len(m.sections) <= 1 {
		return nil
	}
	m.activeWT = (m.activeWT + d + len(m.sections)) % len(m.sections)
	m.commitCursor = 0
	m.commits = nil
	m.logLoaded = false
	m.refreshView()
	if m.mode == ModeFileLog {
		return loadFileLogCmd(m.activeRoot(), m.fileLogPath)
	}
	return loadLogCmd(m.activeRoot())
}

func (m *Model) clearSelectedCommit() {
	m.selectedSHA = ""
	m.selectedShort = ""
	m.selectedSubject = ""
	m.selectedRoot = ""
	m.commitTree = nil
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
	m.selectedRoot = m.activeRoot()
	m.commitTree = nil
	m.cursor = 0
	m.diffCursor = -1
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadCommitTreeCmd(m.selectedRoot, c.SHA)
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

func (m *Model) openFileLog(root, path string) tea.Cmd {
	m.prevTreeMode = m.mode
	m.mode = ModeFileLog
	m.fileLogPath = path
	m.fileLogRoot = root
	m.commits = nil
	m.commitCursor = 0
	m.logLoaded = false
	m.cursor = 0
	m.diffCursor = -1
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadFileLogCmd(root, path)
}

func (m *Model) exitFileLog() tea.Cmd {
	prev := m.prevTreeMode
	if prev != ModeChanged && prev != ModeAll {
		prev = ModeChanged
	}
	m.mode = prev
	m.fileLogPath = ""
	m.fileLogRoot = ""
	m.commits = nil
	m.logLoaded = false
	m.vp.SetYOffset(0)
	return m.reloadAllSections(prev == ModeAll)
}

func (m *Model) refreshCmd() tea.Cmd {
	switch m.mode {
	case ModeChanged:
		return loadInitDataCmd(m.repoRoot, false)
	case ModeAll:
		return loadInitDataCmd(m.repoRoot, true)
	case ModeLog:
		return loadLogCmd(m.activeRoot())
	case ModeFileLog:
		if m.fileLogPath != "" {
			return loadFileLogCmd(m.fileLogRoot, m.fileLogPath)
		}
	case ModeCommit:
		if m.selectedSHA != "" {
			return loadCommitTreeCmd(m.selectedRoot, m.selectedSHA)
		}
	}
	return nil
}

// --- helpers ---

func (m *Model) currentRow() displayRow {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

func (m *Model) currentTreeNode() *tree.Node {
	if r, ok := m.currentRow().(treeRow); ok {
		return r.node
	}
	return nil
}

// currentSectionRoot returns the worktree root of the section the cursor
// currently points into (tree row or header). Falls back to the active
// worktree's root for non-section rows.
func (m *Model) currentSectionRoot() string {
	switch r := m.currentRow().(type) {
	case treeRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return m.sections[r.sectionIdx].WT.Root
		}
		return m.selectedRoot
	case headerRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return m.sections[r.sectionIdx].WT.Root
		}
	}
	return m.activeRoot()
}

func (m *Model) activeRoot() string {
	if m.activeWT >= 0 && m.activeWT < len(m.sections) {
		return m.sections[m.activeWT].WT.Root
	}
	return m.repoRoot
}

// cursorAnchor captures enough about the cursor's current position to find
// it again after the row list is rebuilt. sectionRoot identifies the
// worktree section (so same-named files in different sections don't
// collide); treePath identifies a specific node when the cursor is on a
// tree row, or is empty when the cursor is on a section header.
type cursorAnchor struct {
	sectionRoot string
	treePath    string
}

func (m *Model) cursorAnchor() cursorAnchor {
	switch r := m.currentRow().(type) {
	case treeRow:
		if r.node == nil {
			return cursorAnchor{}
		}
		root := ""
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			root = m.sections[r.sectionIdx].WT.Root
		}
		return cursorAnchor{sectionRoot: root, treePath: r.node.Path}
	case headerRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return cursorAnchor{sectionRoot: m.sections[r.sectionIdx].WT.Root}
		}
	}
	return cursorAnchor{}
}

// restoreCursor relocates the cursor after a row rebuild. It prefers the
// exact (sectionRoot, treePath) tuple, falls back to that section's header
// if the file vanished, and only as a last resort drops to row 0. Without
// the section-root fallback, a refresh that arrives while the cursor is on
// a section header would reset cursor to 0 — visually "jumping to top"
// whenever the user navigates between sections during background activity.
func (m *Model) restoreCursor(a cursorAnchor, prevDiffCursor int) {
	m.cursor = 0
	m.diffCursor = -1
	if a.sectionRoot == "" && a.treePath == "" {
		return
	}
	if a.treePath != "" {
		for i, r := range m.rows {
			t, ok := r.(treeRow)
			if !ok || t.node == nil || t.node.Path != a.treePath {
				continue
			}
			if a.sectionRoot != "" {
				if t.sectionIdx < 0 || t.sectionIdx >= len(m.sections) ||
					m.sections[t.sectionIdx].WT.Root != a.sectionRoot {
					continue
				}
			}
			m.cursor = i
			m.diffCursor = prevDiffCursor
			return
		}
	}
	if a.sectionRoot == "" {
		return
	}
	for i, r := range m.rows {
		h, ok := r.(headerRow)
		if !ok || h.sectionIdx < 0 || h.sectionIdx >= len(m.sections) {
			continue
		}
		if m.sections[h.sectionIdx].WT.Root == a.sectionRoot {
			m.cursor = i
			return
		}
	}
}

func (m *Model) toggle(row displayRow) tea.Cmd {
	switch r := row.(type) {
	case headerRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			s := m.sections[r.sectionIdx]
			wasExpandedWithRows := s.Expanded && s.Root != nil && len(s.Root.Children) > 0
			s.Expanded = !s.Expanded
			m.diffCursor = -1
			m.refreshViewToCursor()
			// Collapsing a section drops a large block of rows; bubbletea's
			// partial-redraw can leak the previously-rendered file row into
			// place above the new content. Force a full repaint to avoid it.
			if wasExpandedWithRows && !s.Expanded {
				return tea.ClearScreen
			}
		}
		return nil
	case treeRow:
		return m.toggleNode(r)
	}
	return nil
}

func (m *Model) toggleNode(r treeRow) tea.Cmd {
	n := r.node
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
		// Track whether collapse will visually drop a non-trivial chunk of
		// rows (a multi-line diff or directory contents). bubbletea's
		// partial-redraw leaves a stale file/dir row visible above the
		// new content in that case; tea.ClearScreen forces a clean repaint.
		shrinking := false
		if n.IsDir {
			shrinking = len(n.Children) > 0
		} else {
			shrinking = len(n.Hunks) > 0 || n.Loading
		}
		n.Expanded = false
		n.Hunks = nil
		n.LoadErr = nil
		m.refreshViewToCursor()
		if shrinking {
			return tea.ClearScreen
		}
		return nil
	}
	n.Expanded = true
	if n.File.Binary {
		m.refreshViewToCursor()
		return nil
	}
	n.Loading = true
	m.refreshViewToCursor()
	root := m.rootForRowSection(r.sectionIdx)
	return loadHunksCmd(root, n.Path, *n.File, m.selectedSHA)
}

// rootForRowSection returns the worktree root to use for git operations
// originating at a treeRow with the given sectionIdx. -1 means "commit-tree
// row"; the active commit's root is used.
func (m *Model) rootForRowSection(sectionIdx int) string {
	if sectionIdx < 0 {
		return m.selectedRoot
	}
	if sectionIdx >= 0 && sectionIdx < len(m.sections) {
		return m.sections[sectionIdx].WT.Root
	}
	return m.repoRoot
}

// positionCursorOnActiveSection places the cursor on the active section's
// header in multi-worktree views so a user launching gdui from a linked
// worktree lands on their own changes, not the main branch's.
func (m *Model) positionCursorOnActiveSection() {
	if !m.showSectionHeaders() {
		return
	}
	for i, r := range m.rows {
		if h, ok := r.(headerRow); ok && h.sectionIdx == m.activeWT {
			m.cursor = i
			return
		}
	}
}

// jumpGroup walks the cursor to the next/previous "grouping" row — either a
// directory tree row OR a section header row. Mirrors the existing folder-
// jump shortcut, extended to treat section headers as super-folders in
// multi-worktree views.
func (m *Model) jumpGroup(dir int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor + dir
	for i >= 0 && i < len(m.rows) {
		switch r := m.rows[i].(type) {
		case headerRow:
			m.cursor = i
			m.refreshViewToCursor()
			return
		case treeRow:
			if r.node != nil && r.node.IsDir {
				m.cursor = i
				m.refreshViewToCursor()
				return
			}
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

func (m *Model) refreshView() {
	// Always rebuild rows for tree/commit modes — cursor positioning logic
	// runs before the first WindowSizeMsg arrives, when m.ready is still
	// false. The viewport-dependent work below is gated separately.
	if m.mode != ModeSearch && m.mode != ModeLog && m.mode != ModeFileLog {
		m.rows = m.flattenForView()
		debugDumpRows(m)
		defer debugDumpFrame(m)
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		if m.diffCursor >= 0 {
			if max := diffNavCount(m.currentTreeNode()); max == 0 {
				m.diffCursor = -1
			} else if m.diffCursor >= max {
				m.diffCursor = max - 1
			}
		}
	}
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
	m.vp.SetContent(m.renderBody())
}

// flattenForView produces the row list for the current tree-mode view —
// either commit tree or working-tree sections.
func (m *Model) flattenForView() []displayRow {
	if m.mode == ModeCommit {
		return flattenCommitTree(m.commitTree)
	}
	return flattenSections(m.sections, m.showSectionHeaders())
}

// showSectionHeaders is true when the UI should render collapsible headers
// per section. Only meaningful in tree modes; suppressed for single-worktree
// repos so the existing single-tree UX is byte-identical.
func (m *Model) showSectionHeaders() bool {
	return len(m.sections) > 1 && (m.mode == ModeChanged || m.mode == ModeAll)
}

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

func (m *Model) cursorY() int {
	y := 0
	for i, r := range m.rows {
		if i == m.cursor {
			break
		}
		y++
		if t, ok := r.(treeRow); ok && t.node != nil && !t.node.IsDir && t.node.Expanded {
			y += hunkLineCount(t.node)
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

func hunkLineCount(n *tree.Node) int {
	if n.File != nil && n.File.Binary {
		return 1
	}
	if n.Loading || n.LoadErr != nil {
		return 1
	}
	c := render.HunkLineCount(n.Hunks)
	if c == 0 {
		return 1
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
	wtCrumb := m.worktreeCrumb()
	if m.mode == ModeLog {
		left := fmt.Sprintf("[log] %d commits", len(m.commits))
		return helpStyle.Render(left+wtCrumb+" · enter open · a toggle · ? help") +
			helpStyle.Render(m.tabHint())
	}
	if m.mode == ModeFileLog {
		left := fmt.Sprintf("[file log] %d commits · %s", len(m.commits), m.fileLogPath)
		return helpStyle.Render(left + wtCrumb + " · enter open · esc back · ? help")
	}

	// ModeChanged / ModeAll / ModeCommit all show file count + adds/dels totals.
	files, adds, dels := m.aggregateRowStats()
	totals := addsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", dels))
	var left, hint string
	if m.mode == ModeCommit {
		left = fmt.Sprintf("[commit %s] %d files", m.selectedShort, files)
		hint = wtCrumb + " · esc back · ? help"
	} else {
		left = fmt.Sprintf("[%s] %d changed", m.mode.String(), files)
		hint = wtCrumb + " · a toggle · ? help"
	}
	return helpStyle.Render(left+" · ") + totals + helpStyle.Render(hint)
}

// worktreeCrumb formats " · [worktree: <name>]" when there's more than one
// worktree to disambiguate. Returns "" otherwise so the footer stays clean
// for single-worktree repos.
func (m Model) worktreeCrumb() string {
	if len(m.sections) <= 1 {
		return ""
	}
	name := ""
	switch m.mode {
	case ModeLog, ModeFileLog, ModeCommit:
		if m.activeWT >= 0 && m.activeWT < len(m.sections) {
			name = m.sections[m.activeWT].WT.Branch
		}
	default:
		// Tree modes: cursor's section, fallback to active.
		if r, ok := m.currentRow().(treeRow); ok && r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			name = m.sections[r.sectionIdx].WT.Branch
		} else if r, ok := m.currentRow().(headerRow); ok && r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			name = m.sections[r.sectionIdx].WT.Branch
		} else if m.activeWT >= 0 && m.activeWT < len(m.sections) {
			name = m.sections[m.activeWT].WT.Branch
		}
	}
	if name == "" {
		return ""
	}
	return " · [worktree: " + name + "]"
}

func (m Model) tabHint() string {
	if len(m.sections) <= 1 {
		return ""
	}
	return " · tab next worktree"
}

func (m Model) aggregateRowStats() (files, adds, dels int) {
	if m.mode == ModeCommit {
		if m.commitTree != nil {
			adds, dels = m.commitTree.Adds, m.commitTree.Dels
		}
		for _, r := range m.rows {
			if t, ok := r.(treeRow); ok && t.node != nil && !t.node.IsDir && t.node.File != nil {
				files++
			}
		}
		return
	}
	for _, s := range m.sections {
		if s.Root != nil {
			adds += s.Root.Adds
			dels += s.Root.Dels
		}
		for _, f := range s.Files {
			_ = f
			files++
		}
	}
	return
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
	for i, r := range m.rows {
		switch row := r.(type) {
		case headerRow:
			rendered := m.fitWidth(m.renderSectionHeader(i, row.sectionIdx))
			b.WriteString(m.zones.Mark(headerZoneID(row.sectionIdx), rendered))
			b.WriteByte('\n')
		case treeRow:
			rendered := m.fitWidth(m.renderTreeRow(i, row.node))
			b.WriteString(m.zones.Mark(zoneID(i), rendered))
			b.WriteByte('\n')
			if row.node != nil && !row.node.IsDir && row.node.Expanded {
				b.WriteString(m.fitLines(m.renderExpanded(row.node)))
				b.WriteByte('\n')
			}
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

func (m *Model) fitWidth(line string) string {
	if m.width <= 0 || lipgloss.Width(line) <= m.width {
		return line
	}
	return render.TruncateANSI(line, m.width)
}

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

func (m *Model) applyCursor(row string, selected bool) string {
	if !selected {
		return row
	}
	if pad := m.width - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return cursorStyle.Render(row)
}

func (m *Model) renderTreeRow(i int, n *tree.Node) string {
	if n == nil {
		return ""
	}
	depth := tree.Depth(n)
	if m.showSectionHeaders() {
		depth++ // indent under the section header
	}
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

func (m *Model) renderSectionHeader(rowIdx, sectionIdx int) string {
	if sectionIdx < 0 || sectionIdx >= len(m.sections) {
		return ""
	}
	s := m.sections[sectionIdx]
	chev := "▾"
	if !s.Expanded {
		chev = "▸"
	}
	name := s.WT.Branch
	if name == "" {
		name = "(no branch)"
	} else if name == "(detached)" && len(s.WT.HEAD) >= 7 {
		name = "(detached @" + s.WT.HEAD[:7] + ")"
	}
	mods, untr := 0, 0
	for _, f := range s.Files {
		if f.Kind == git.Untracked {
			untr++
		} else {
			mods++
		}
	}
	var counts string
	if s.LoadErr != nil {
		counts = errStyle.Render("(error)")
	} else if s.Root == nil {
		counts = dimStyle.Render("(loading…)")
	} else if mods == 0 && untr == 0 {
		counts = dimStyle.Render("(clean)")
	} else {
		var parts []string
		if mods > 0 {
			parts = append(parts, fmt.Sprintf("M:%d", mods))
		}
		if untr > 0 {
			parts = append(parts, fmt.Sprintf("?:%d", untr))
		}
		counts = dimStyle.Render("(" + strings.Join(parts, " ") + ")")
	}
	tail := ""
	if !s.Expanded {
		tail += "  " + dimStyle.Render("[collapsed]")
	}
	if s.WT.Locked {
		tail += "  " + dimStyle.Render("[locked]")
	}
	if s.WT.Prunable {
		tail += "  " + dimStyle.Render("[prunable]")
	}
	row := fmt.Sprintf("%s %s  %s%s", chev, dirStyle.Render(name), counts, tail)
	return m.applyCursor(row, rowIdx == m.cursor)
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
	if cur := m.currentTreeNode(); cur == n {
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
			{"h / ←", "collapse (or jump to parent / section)"},
			{"l / →", "expand"},
			{"[ / ]", "previous / next folder or worktree"},
			{"g / G", "top / bottom"},
			{"ctrl+u / ctrl+d", "page up / down"},
		}},
		{"Open & inspect", []row{
			{"enter / space", "toggle expand/collapse (or open commit)"},
			{"b", "file history (commits touching this file)"},
			{"left-click row", "toggle expand/collapse"},
			{"scroll wheel", "scroll viewport"},
		}},
		{"Worktrees", []row{
			{"tab / ⇧tab", "next / prev worktree (in log mode)"},
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

func zoneID(i int) string             { return fmt.Sprintf("row-%d", i) }
func commitZoneID(i int) string       { return fmt.Sprintf("commit-%d", i) }
func headerZoneID(sectionIdx int) string { return fmt.Sprintf("wt-%d", sectionIdx) }

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
