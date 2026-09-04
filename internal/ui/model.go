package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/andreas-bergstrom/gdui/internal/drop"
	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/render"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

const (
	// logLimit caps the size of ModeFileLog's flat history list.
	logLimit = 100
	// logPageSize is the per-section batch size for ModeLog's paged loader.
	// Small enough that the initial render fits on one screen for typical
	// repos; auto-load-more fetches the next batch as the cursor approaches
	// the bottom.
	logPageSize = 10
	// autoLoadMoreThreshold is how close the cursor can get to the end of a
	// section's loaded commits before we proactively fetch the next page.
	autoLoadMoreThreshold = 3
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
	ModeBranchPicker
	ModeBranchDiff
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
	case ModeBranchPicker:
		return "branch picker"
	case ModeBranchDiff:
		return "branch diff"
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

	// nestedChildren maps each parent worktree's absolute root to the list
	// of relative paths of nested git repos that live directly under it.
	// Used to exclude those paths from the parent's status (they'd otherwise
	// appear as opaque untracked directories). Populated by applyInitData;
	// consulted by loadStatusCmd via excludePathsFor.
	nestedChildren map[string][]string

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

	// filter holds the active tree-filter state. Lives globally on the
	// model: tree modes consult it; non-tree modes simply don't, so the
	// filter survives a quick `b → esc` round-trip into ModeFileLog and back.
	filter filterState

	// drop holds the drag-and-drop import state. While the prompt is up,
	// handleKey routes to handleKeyDrop. Cleared on mode transitions out
	// of tree modes — see dropResetOnModeChange.
	drop dropState

	// revert holds the "revert to HEAD" confirmation state. Same gate
	// pattern as drop — see revertResetOnModeChange.
	revert revertState

	// branchPicker is the overlay state for picking a ref. Active only
	// while mode == ModeBranchPicker.
	branchPicker branchPickerState
	// branchDiff is the tree view state for ModeBranchDiff. Mirrors the
	// commitTree/selected* fields' pattern but for ref-to-HEAD diffs.
	branchDiff branchDiffState

	// pendingDropTarget tells the next statusMsg handler where to place the
	// cursor instead of restoring the pre-refresh position. Set in the
	// dropCompletedMsg handler, consumed (and cleared) by statusMsg. Without
	// this, the cursor would snap back to the file the user was looking at
	// before the drop, not to the newly-imported file.
	pendingDropTarget cursorAnchor

	// toast is a transient one-line message shown in the footer (e.g.
	// "reloaded" after `r`). Cleared automatically when toastSeq stops
	// matching after toastDuration; sequence-matched so a later toast
	// can't get blown away by an earlier expiration tick.
	toast     string
	toastSeq  int
	toastTime time.Time

	// restartWatchers is invoked from the manual-refresh path to stop the
	// current fs watchers and respawn them against the freshly-discovered
	// worktree + nested-repo list. Wired in from main.go via New so the
	// model doesn't need to import internal/watch. Nil-safe: nil means no
	// watcher restart (used in tests).
	restartWatchers func()
}

func New(repoRoot string, restartWatchers func()) Model {
	return Model{
		repoRoot:        repoRoot,
		mode:            ModeChanged,
		zones:           zone.New(),
		diffCursor:      -1,
		restartWatchers: restartWatchers,
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
	// nested is the subset of worktrees that were discovered by walking the
	// working tree (nested git repos / submodules), as opposed to those
	// returned by `git worktree list` on the launch repo. Keyed by Root.
	nested   map[string]bool
	statuses map[string]sectionStatus
	allMode  bool
	err      error
}

type sectionStatus struct {
	files         []git.ChangedFile
	tree          *tree.Node
	unpushed      map[string]bool
	unpushedCount int
	hasRemotes    bool
	err           error
}

type statusMsg struct {
	root          string
	files         []git.ChangedFile
	tree          *tree.Node
	unpushed      map[string]bool
	unpushedCount int
	hasRemotes    bool
	err           error
}

type logMsg struct {
	root string
	// skip is the offset this batch was loaded from. skip==0 means "page 1
	// — replace the section's LogCommits"; skip>0 means "append starting
	// at this offset". On mismatch (skip != len(section.LogCommits) for
	// appends, or stale gen) the message is dropped to defend against
	// races between auto-load-more and watcher-driven first-page reloads.
	skip    int
	gen     int
	commits []git.Commit
	// dest distinguishes a multi-section ModeLog batch (DestSectionLog)
	// from a single-section ModeFileLog batch (DestFileLog). The handlers
	// have very different routing rules (per-section state on the section
	// vs. the model-level fileLog cursor), so we keep them separate to
	// avoid accidental cross-talk.
	dest logDest
	err  error
	// Push status, populated only on page-1 section loads (skip==0).
	unpushed      map[string]bool
	unpushedCount int
	hasRemotes    bool
}

type logDest int

const (
	logDestSection logDest = iota
	logDestFile
)

type commitTreeMsg struct {
	root string
	sha  string
	tree *tree.Node
	err  error
}

type infoToastExpiredMsg struct {
	seq int
}

// setInfoToast stores a short transient message and returns a Cmd that fires
// an expiration tick. Use for "happened-and-finished" UX confirmations like
// manual refresh. The seq match in the handler ensures rapid successive
// toasts don't clobber each other.
func (m *Model) setInfoToast(text string) tea.Cmd {
	m.toastSeq++
	m.toast = text
	m.toastTime = time.Now()
	seq := m.toastSeq
	return tea.Tick(toastDuration, func(time.Time) tea.Msg {
		return infoToastExpiredMsg{seq: seq}
	})
}

type hunksMsg struct {
	root string
	// ref is set for ModeBranchDiff hunk loads (the source branch of the
	// merge-base diff). Empty for working-tree and commit hunks.
	ref   string
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
		// Append nested git repos (independent or submodules) found under the
		// launch repo's worktrees so each becomes its own section.
		nestedRepos := git.DiscoverNestedReposRecursive(wts, 0)
		nested := make(map[string]bool, len(nestedRepos))
		for _, n := range nestedRepos {
			nested[n.Root] = true
		}
		all := append([]git.Worktree(nil), wts...)
		all = append(all, nestedRepos...)
		// Build a per-parent map of nested child relative paths so each
		// parent's status excludes its nested repos (otherwise git would
		// surface them as opaque untracked directories, polluting the
		// parent's tree).
		childPaths := nestedChildPathsMap(all)
		statuses := map[string]sectionStatus{}
		for _, wt := range all {
			statuses[wt.Root] = loadSectionStatus(wt.Root, allMode, childPaths[wt.Root])
		}
		return initDataMsg{worktrees: all, nested: nested, statuses: statuses, allMode: allMode}
	}
}

// nestedChildPathsMap returns, for each worktree root, the list of paths
// (relative to that root) of other sections that live directly inside it —
// discovered nested repos and in-tree linked worktrees alike. Repos nested
// several levels deep attach to the innermost parent. Used to filter each
// section's ChangedFile and ListAll paths so a child repo's directory doesn't
// appear as an opaque untracked entry in its parent's tree.
func nestedChildPathsMap(all []git.Worktree) map[string][]string {
	out := map[string][]string{}
	// Collect candidate parent roots (everything that could host a nested
	// repo — both linked worktrees and nested repos that have repos
	// themselves underneath them).
	parents := make([]string, 0, len(all))
	for _, w := range all {
		parents = append(parents, filepath.Clean(w.Root))
	}
	for _, w := range all {
		childAbs := filepath.Clean(w.Root)
		// Pick the longest parent path that's a strict ancestor of this
		// nested root — that's the section this child should attach to.
		best := ""
		for _, p := range parents {
			if p == childAbs {
				continue
			}
			if !strings.HasPrefix(childAbs, p+string(filepath.Separator)) {
				continue
			}
			if len(p) > len(best) {
				best = p
			}
		}
		if best == "" {
			continue
		}
		if rel, err := filepath.Rel(best, childAbs); err == nil {
			// Normalize to forward slashes — git always emits paths with
			// '/' regardless of OS (we pass core.quotepath=false to status),
			// while filepath.Rel uses the OS separator. Without this, the
			// filter would silently miss matches on Windows.
			out[best] = append(out[best], filepath.ToSlash(rel))
		}
	}
	return out
}

func loadStatusCmd(root string, allMode bool, excludeRelPaths []string) tea.Cmd {
	return func() tea.Msg {
		st := loadSectionStatus(root, allMode, excludeRelPaths)
		return statusMsg{
			root: root, files: st.files, tree: st.tree,
			unpushed: st.unpushed, unpushedCount: st.unpushedCount, hasRemotes: st.hasRemotes,
			err: st.err,
		}
	}
}

// loadSectionStatus loads one section's status, optionally filtering out
// ChangedFile entries whose paths match any of excludeRelPaths. Used to
// drop nested-repo directories from a parent section's tree (they appear
// as opaque untracked entries in `git status` since git won't descend into
// a directory containing its own .git).
func loadSectionStatus(root string, allMode bool, excludeRelPaths []string) sectionStatus {
	files, err := git.Status(root)
	if err != nil {
		return sectionStatus{err: err}
	}
	files = filterChangedFiles(files, excludeRelPaths)
	unpushed, count, _ := git.Unpushed(root)
	hasRemotes := git.HasRemotes(root)
	if !allMode {
		return sectionStatus{files: files, tree: tree.Build(files),
			unpushed: unpushed, unpushedCount: count, hasRemotes: hasRemotes}
	}
	all, err := git.ListAll(root)
	if err != nil {
		return sectionStatus{files: files, err: err,
			unpushed: unpushed, unpushedCount: count, hasRemotes: hasRemotes}
	}
	all = filterPaths(all, excludeRelPaths)
	return sectionStatus{files: files, tree: tree.BuildAll(files, all),
		unpushed: unpushed, unpushedCount: count, hasRemotes: hasRemotes}
}

// filterChangedFiles removes entries whose Path equals any excludePath (or
// is rooted at a directory equal to one). Returns the input unchanged when
// excludePaths is empty (common case for nested-repo sections themselves).
func filterChangedFiles(files []git.ChangedFile, excludePaths []string) []git.ChangedFile {
	if len(excludePaths) == 0 {
		return files
	}
	out := files[:0:0]
	for _, f := range files {
		if pathExcluded(f.Path, excludePaths) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func filterPaths(paths []string, excludePaths []string) []string {
	if len(excludePaths) == 0 {
		return paths
	}
	out := paths[:0:0]
	for _, p := range paths {
		if pathExcluded(p, excludePaths) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func pathExcluded(p string, excludePaths []string) bool {
	for _, ex := range excludePaths {
		if p == ex || strings.HasPrefix(p, ex+"/") {
			return true
		}
	}
	return false
}

// loadLogCmd loads one page of commits for a section's log. gen is the
// section's LogReloadGen at dispatch time; the handler drops the message
// when the section's gen has been bumped (manual refresh / watcher event)
// while this load was in flight.
func loadLogCmd(root string, skip, gen int) tea.Cmd {
	return func() tea.Msg {
		c, err := git.Log(root, logPageSize, skip)
		m := logMsg{root: root, skip: skip, gen: gen, commits: c, err: err, dest: logDestSection}
		if skip == 0 {
			// Refresh push status on page-1 loads so ModeLog marks stay current
			// after a commit made while viewing the log.
			m.unpushed, m.unpushedCount, _ = git.Unpushed(root)
			m.hasRemotes = git.HasRemotes(root)
		}
		return m
	}
}

// reloadSectionLog clears the section's log state, bumps its reload
// generation, and dispatches a fresh first-page load. Used by mode entry
// for the active section, by header-expand for collapsed sections, and by
// RefreshMsg for the section a watcher event referenced.
func reloadSectionLog(s *WorktreeSection) tea.Cmd {
	s.LogReloadGen++
	s.LogCommits = nil
	s.LogLoaded = false
	s.LogHasMore = false
	s.LogLoading = true
	return loadLogCmd(s.WT.Root, 0, s.LogReloadGen)
}

func loadFileLogCmd(root, path string) tea.Cmd {
	return func() tea.Msg {
		c, err := git.LogForPath(root, path, logLimit)
		return logMsg{root: root, commits: c, err: err, dest: logDestFile}
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
			return m, loadStatusCmd(msg.Root, m.mode == ModeAll, m.nestedChildren[msg.Root])
		case ModeLog:
			// Per-section reload: a watcher event names exactly one
			// worktree's HEAD log dir. Reload only that section's first
			// page. Empty Root means a manual full refresh — reload every
			// section's first page so paged offsets reset everywhere.
			if msg.Root != "" {
				if idx := findSectionByRoot(m.sections, msg.Root); idx >= 0 {
					return m, reloadSectionLog(m.sections[idx])
				}
				return m, nil
			}
			cmds := make([]tea.Cmd, 0, len(m.sections))
			for _, s := range m.sections {
				if !s.LogLoaded && !s.LogLoading {
					continue // collapsed / never-loaded sections stay lazy
				}
				cmds = append(cmds, reloadSectionLog(s))
			}
			return m, tea.Batch(cmds...)
		case ModeFileLog:
			if m.fileLogPath != "" {
				return m, loadFileLogCmd(m.fileLogRoot, m.fileLogPath)
			}
		case ModeSearch:
			return m, loadSearchPathsCmd(m.activeRoot())
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
			// If the worktree directory itself is gone (removed externally
			// via `git worktree remove` while gdui is running), drop the
			// section instead of leaving it stuck in an "(error)" state.
			// The fs watcher for that root will stop emitting once its
			// goroutine sees the missing dir; until then any further
			// RefreshMsg{Root: <gone>} no-ops since findSectionByRoot will
			// return -1.
			if _, statErr := os.Stat(msg.root); os.IsNotExist(statErr) {
				m.dropSection(idx)
				m.refreshView()
				return m, nil
			}
			s.LoadErr = msg.err
			s.Files = nil
			s.Root = nil
			m.refreshView()
			return m, nil
		}
		// Preserve in-section expand state across reloads.
		preserveTreeState(s.Root, msg.tree)
		prevAnchor := m.cursorAnchor()
		// A drop just finished: target the new file instead of restoring
		// the pre-refresh cursor. Pre-expanding ancestors here ensures the
		// row is actually flattened into view.
		if m.pendingDropTarget.sectionRoot == s.WT.Root && m.pendingDropTarget.treePath != "" {
			prevAnchor = m.pendingDropTarget
			expandToPath(msg.tree, m.pendingDropTarget.treePath)
			m.pendingDropTarget = cursorAnchor{}
		}
		s.Files = msg.files
		s.Root = msg.tree
		s.UnpushedSHAs = msg.unpushed
		s.UnpushedCount = msg.unpushedCount
		s.HasRemotes = msg.hasRemotes
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
		if msg.dest == logDestFile {
			// ModeFileLog flat history — single-section as before.
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
		}
		// Per-section paged log batch (ModeLog).
		idx := findSectionByRoot(m.sections, msg.root)
		if idx < 0 {
			return m, nil // section removed mid-flight
		}
		s := m.sections[idx]
		if msg.gen != s.LogReloadGen {
			// Superseded by a refresh; the freshly-reset state must win.
			return m, nil
		}
		if msg.err != nil {
			s.LogLoading = false
			s.LogLoaded = true
			m.err = msg.err
			m.refreshView()
			return m, nil
		}
		m.err = nil
		prevAnchor := m.cursorAnchor()
		if msg.skip == 0 {
			s.LogCommits = msg.commits
			s.UnpushedSHAs = msg.unpushed
			s.UnpushedCount = msg.unpushedCount
			s.HasRemotes = msg.hasRemotes
		} else {
			// Defensive: only append when the offset matches what we've
			// already loaded. Anything else means the load order skewed
			// against our state; drop rather than splice mismatched commits.
			if msg.skip != len(s.LogCommits) {
				s.LogLoading = false
				return m, nil
			}
			s.LogCommits = append(s.LogCommits, msg.commits...)
		}
		s.LogHasMore = len(msg.commits) == logPageSize
		s.LogLoaded = true
		s.LogLoading = false
		if m.mode == ModeLog {
			m.refreshView()
			m.restoreCursor(prevAnchor, -1)
			m.ensureCursorVisible()
		} else {
			// View isn't currently displaying this batch; still refresh in
			// case status counts or footer aggregates use LogCommits.
			m.refreshView()
		}
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
		// In commit mode, look up in m.commitTree; in branch-diff, in
		// m.branchDiff.tr; otherwise look up in the originating section.
		switch {
		case m.mode == ModeCommit && msg.root == m.selectedRoot:
			n = tree.FindByPath(m.commitTree, msg.path)
		case m.mode == ModeBranchDiff && msg.root == m.branchDiff.root && msg.ref == m.branchDiff.ref:
			n = tree.FindByPath(m.branchDiff.tr, msg.path)
		default:
			if idx := findSectionByRoot(m.sections, msg.root); idx >= 0 {
				n = tree.FindByPath(m.sections[idx].Root, msg.path)
			}
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

	case infoToastExpiredMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
		}
		return m, nil

	case branchListMsg:
		if m.mode != ModeBranchPicker || msg.root != m.branchPicker.root {
			return m, nil // stale
		}
		m.branchPicker.loaded = true
		m.branchPicker.err = msg.err
		m.branchPicker.all = msg.branches
		m.refilterBranches()
		m.refreshView()
		return m, nil

	case branchTreeMsg:
		if m.mode != ModeBranchDiff || msg.root != m.branchDiff.root || msg.ref != m.branchDiff.ref {
			return m, nil // stale
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.branchDiff.tr = msg.tr
		m.cursor = 0
		m.refreshView()
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

	case dropCompletedMsg:
		root := msg.root
		relPath, err := filepath.Rel(root, msg.dest)
		if err != nil || relPath == "." || strings.HasPrefix(relPath, "..") {
			// dest fell outside root somehow; nothing to anchor to.
			return m, m.advanceDropQueue()
		}
		// Tree paths use forward slashes regardless of OS — normalize.
		relPath = filepath.ToSlash(relPath)
		// Mark the in-flight drop's destination so the next statusMsg lands
		// the cursor there. expandToPath is also called on the current
		// section root so the directory chain is open BEFORE the reload; the
		// per-Path Expanded carries over via preserveTreeState.
		m.pendingDropTarget = cursorAnchor{sectionRoot: root, treePath: relPath}
		if idx := findSectionByRoot(m.sections, root); idx >= 0 {
			m.activeWT = idx
			if m.sections[idx].Root != nil {
				expandToPath(m.sections[idx].Root, relPath)
			}
		}
		// Advance the queue first (may prompt for the next drop), then
		// reload the destination section's status.
		advance := m.advanceDropQueue()
		reload := loadStatusCmd(root, m.mode == ModeAll, m.nestedChildren[root])
		return m, tea.Batch(advance, reload)

	case revertCompletedMsg:
		root := msg.root
		m.revert = revertState{}
		if m.mode == ModeChanged || m.mode == ModeAll {
			return m, tea.Batch(
				loadStatusCmd(root, m.mode == ModeAll, m.nestedChildren[root]),
				tea.ClearScreen,
			)
		}
		return m, tea.ClearScreen

	case revertFailedMsg:
		m.revert = revertState{err: msg.err.Error()}
		m.refreshView()
		return m, tea.ClearScreen

	case dropFailedMsg:
		// Pop the failed drop off the queue (queue[0] was the one being
		// copied) and surface the error in the status row. Subsequent
		// queued drops still get their own prompt.
		if len(m.drop.queue) > 0 {
			m.drop.queue = m.drop.queue[1:]
		}
		m.drop.err = msg.err.Error()
		return m, m.advanceDropQueue()

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
	// Collect non-nested worktree roots up front so nested section labels can
	// be computed as "path relative to the parent worktree this nested repo
	// lives under" (the longest non-nested root that's a prefix).
	parentRoots := make([]string, 0, len(msg.worktrees))
	for _, wt := range msg.worktrees {
		if !msg.nested[wt.Root] {
			parentRoots = append(parentRoots, wt.Root)
		}
	}
	// Refresh the parent→nested-children map so per-section refreshes
	// (RefreshMsg-driven) can keep filtering nested-repo dirs out of their
	// parent's status.
	m.nestedChildren = nestedChildPathsMap(msg.worktrees)
	newSecs := make([]*WorktreeSection, 0, len(msg.worktrees))
	for _, wt := range msg.worktrees {
		st := msg.statuses[wt.Root]
		var prevSec *WorktreeSection
		if i := findSectionByRoot(prev, wt.Root); i >= 0 {
			prevSec = prev[i]
		}
		s := &WorktreeSection{WT: wt, Nested: msg.nested[wt.Root]}
		if s.Nested {
			s.Label = nestedSectionLabel(wt.Root, parentRoots)
		}
		if prevSec != nil {
			s.Expanded = prevSec.Expanded
			s.firstLoadDone = prevSec.firstLoadDone
			preserveTreeState(prevSec.Root, st.tree)
		} else {
			s.Expanded = wt.Root == m.repoRoot // active worktree opens by default
		}
		s.Files = st.files
		s.Root = st.tree
		s.UnpushedSHAs = st.unpushed
		s.UnpushedCount = st.unpushedCount
		s.HasRemotes = st.hasRemotes
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

// dropSection removes the section at idx and adjusts activeWT so it still
// points at a valid section (or 0 if the list is now empty). Used when a
// worktree disappears externally — its status reload fails with a missing
// directory and the UI cleans up rather than parking the section in an
// "(error)" state forever. Note: the watcher goroutine for the removed root
// is not stopped here; further RefreshMsgs for it are dropped by the
// findSectionByRoot guard in the statusMsg handler.
func (m *Model) dropSection(idx int) {
	if idx < 0 || idx >= len(m.sections) {
		return
	}
	m.sections = append(m.sections[:idx], m.sections[idx+1:]...)
	switch {
	case len(m.sections) == 0:
		m.activeWT = 0
	case m.activeWT > idx:
		m.activeWT--
	case m.activeWT == idx && m.activeWT >= len(m.sections):
		m.activeWT = len(m.sections) - 1
	}
	// Cursor was anchored to a row in the now-removed section. refreshView
	// will clamp m.cursor to len(rows)-1 if it overshoots, but that lands
	// the cursor on a semantically unrelated row. Resetting to 0 puts it
	// at a predictable position (first header of the next section).
	m.cursor = 0
	m.diffCursor = -1
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
	switch {
	case m.mode == ModeCommit && m.selectedRoot != "" && m.commitTree != nil:
		walk(m.selectedRoot, m.selectedSHA, m.commitTree)
	case m.mode == ModeBranchDiff && m.branchDiff.root != "" && m.branchDiff.tr != nil:
		// Branch-diff uses a different hunk loader; walk the tree and queue
		// per-file ref-hunk loads inline rather than reusing the closure.
		var visit func(*tree.Node)
		visit = func(n *tree.Node) {
			if !n.IsDir && n.Expanded && n.File != nil && !n.File.Binary {
				cmds = append(cmds, loadBranchHunksCmd(m.branchDiff.root, m.branchDiff.ref, n.Path))
			}
			for _, c := range n.Children {
				visit(c)
			}
		}
		visit(m.branchDiff.tr)
	default:
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
	// Drop detection: bracketed paste (Terminal.app, iTerm2, kitty, etc.)
	// arrives as a KeyMsg with msg.Paste=true. Terminals that DON'T support
	// bracketed paste (notably Warp) just type the dropped path as plain
	// rune input — Bubble Tea batches consecutive printable runes into one
	// KeyMsg, so a multi-rune KeyRunes with Paste=false is our fallback
	// signal. We require ≥4 runes to avoid mistaking fast typing for a drop;
	// drop.Parse's strict stat check filters out anything that isn't a real
	// file. The remaining false-positive is the user pasting plain text that
	// is exactly a valid file path with no other content — recoverable via
	// Esc on the resulting prompt.
	const dropMinRunes = 4
	maybeDrop := msg.Paste || (msg.Type == tea.KeyRunes && len(msg.Runes) >= dropMinRunes)
	if maybeDrop && (m.mode == ModeChanged || m.mode == ModeAll) {
		payload := string(msg.Runes)
		if paths := drop.Parse(payload); len(paths) > 0 {
			return *m, m.handleDropPaste(paths)
		}
		// Drop didn't parse but the burst LOOKS like the start of an
		// absolute path whose parent directory exists. Most likely
		// explanation: a Warp-style drop with a space in the filename —
		// Bubble Tea's input parser breaks rune batches on spaces, so we
		// only ever saw the first chunk. Surface a one-shot error in the
		// drop status row so the user understands why nothing happened.
		// Esc clears the error (handled below when drop is idle).
		if !msg.Paste && looksLikeTruncatedDropPath(payload) {
			m.drop.err = "path likely contains a space — needs bracketed paste (Terminal.app, iTerm2, kitty)"
			m.refreshView()
			return *m, tea.ClearScreen
		}
	}

	// Esc dismisses a sticky drop error when there's no active prompt.
	// Without this the warning persists indefinitely once shown.
	if msg.Type == tea.KeyEsc && m.drop.phase == dropIdle && m.drop.err != "" {
		m.drop.err = ""
		m.refreshView()
		return *m, tea.ClearScreen
	}

	// Drop-prompt input gate: while the destination/overwrite prompt is up,
	// swallow keys other than Ctrl+C so global bindings (q, r, a, etc.)
	// don't fire mid-prompt. Above filter/search gates so an accidental
	// drop while one of those is open still routes to drop.
	if m.drop.active() {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m.handleKeyDrop(msg)
	}

	// Revert-prompt gate. Drop and revert are mutually exclusive — only one
	// can be active at a time. Esc on a sticky revert error clears it the
	// same way the drop branch above handles m.drop.err.
	if msg.Type == tea.KeyEsc && m.revert.phase == revertIdle && m.revert.err != "" {
		m.revert.err = ""
		m.refreshView()
		return *m, tea.ClearScreen
	}
	if m.revert.active() {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m.handleKeyRevert(msg)
	}

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

	if m.mode == ModeBranchPicker {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m.handleKeyBranchPicker(msg)
	}

	// Filter-editing gate: while typing in the filter input, swallow keys
	// other than Ctrl+C (still quits) so global bindings like `q` and `r`
	// don't fire mid-pattern. Same shape as the ModeSearch gate above —
	// the two are mutually exclusive (ModeSearch never coexists with
	// filter editing because entering search uses a separate mode and `f`
	// is dispatched via handleKeyTree).
	if m.filter.editing {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m.handleKeyFilter(msg)
	}

	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return *m, tea.Batch(m.refreshCmd(), m.setInfoToast("reloaded"))
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
		if m.mode == ModeBranchDiff {
			return *m, m.exitBranchDiff()
		}
		return *m, nil
	case key.Matches(msg, keys.Branches):
		return *m, m.enterBranchPicker()
	}

	if m.mode == ModeLog || m.mode == ModeFileLog {
		return m.handleKeyLog(msg)
	}
	return m.handleKeyTree(msg)
}

func (m *Model) handleKeyTree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prevRow := m.currentRow()
	switch {
	case key.Matches(msg, keys.Filter):
		return *m, m.enterFilterEdit()
	case key.Matches(msg, keys.NextWorktree):
		if m.showSectionHeaders() {
			return *m, m.jumpSectionWithRepaint(prevRow, 1)
		}
	case key.Matches(msg, keys.PrevWorktree):
		if m.showSectionHeaders() {
			return *m, m.jumpSectionWithRepaint(prevRow, -1)
		}
	case key.Matches(msg, keys.Blame):
		n := m.currentTreeNode()
		if n == nil || n.IsDir || n.File == nil {
			return *m, nil
		}
		return *m, m.openFileLog(m.currentSectionRoot(), n.Path)
	case key.Matches(msg, keys.Revert):
		return *m, m.initRevert()
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
	if m.mode == ModeLog {
		// Unified-rows path: ModeLog now uses m.cursor / m.rows like the
		// tree modes do. Tab jumps between section headers (auto-expanding
		// and loading) rather than cycling an "active" worktree.
		switch {
		case key.Matches(msg, keys.NextWorktree):
			return *m, m.jumpToSectionHeader(1)
		case key.Matches(msg, keys.PrevWorktree):
			return *m, m.jumpToSectionHeader(-1)
		case key.Matches(msg, keys.Down):
			m.moveCursor(1)
			return *m, m.maybeAutoLoadMore()
		case key.Matches(msg, keys.Up):
			m.moveCursor(-1)
			return *m, m.maybeAutoLoadMore()
		case key.Matches(msg, keys.PgDn):
			m.moveCursor(m.vp.Height / 2)
			return *m, m.maybeAutoLoadMore()
		case key.Matches(msg, keys.PgUp):
			m.moveCursor(-m.vp.Height / 2)
			return *m, m.maybeAutoLoadMore()
		case key.Matches(msg, keys.Top):
			m.cursor = 0
			m.refreshViewToCursor()
		case key.Matches(msg, keys.Bottom):
			m.cursor = len(m.rows) - 1
			m.refreshViewToCursor()
			return *m, m.maybeAutoLoadMore()
		case key.Matches(msg, keys.Toggle), key.Matches(msg, keys.Right):
			return *m, m.openCommit()
		}
		return *m, nil
	}
	// ModeFileLog — flat single-section list with m.commits/m.commitCursor.
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

// jumpSectionWithRepaint wraps jumpToSectionHeader with the bubbletea
// partial-redraw workaround documented in CLAUDE.md: crossing between a
// tree row and a section header leaves stale cursor highlight unless we
// force a full repaint. The early-returning Tab cases bypass the
// needsRepaintAfterMove check at the bottom of handleKeyTree, so apply
// it here at the call site instead.
func (m *Model) jumpSectionWithRepaint(prev displayRow, d int) tea.Cmd {
	cmd := m.jumpToSectionHeader(d)
	if needsRepaintAfterMove(prev, m.currentRow()) {
		if cmd == nil {
			return tea.ClearScreen
		}
		return tea.Batch(cmd, tea.ClearScreen)
	}
	return cmd
}

// jumpToSectionHeader moves the cursor to the next/prev section header
// (wrapping at boundaries). If the landed-on section is collapsed, expand
// it and trigger a first-page load on the next render. Used by Tab/⇧Tab
// in ModeLog.
func (m *Model) jumpToSectionHeader(d int) tea.Cmd {
	if len(m.rows) == 0 {
		return nil
	}
	n := len(m.rows)
	// Walk one row at a time in direction d, starting from cursor+d, until
	// we find a headerRow or come back to where we started. Using a small
	// state machine instead of (cursor + d*steps) avoids the off-by-one
	// risk and keeps modular arithmetic readable.
	for steps, i := 0, (((m.cursor+d)%n)+n)%n; steps < n; steps, i = steps+1, (((i+d)%n)+n)%n {
		h, ok := m.rows[i].(headerRow)
		if !ok {
			continue
		}
		if h.sectionIdx < 0 || h.sectionIdx >= len(m.sections) {
			continue
		}
		s := m.sections[h.sectionIdx]
		m.cursor = i
		var cmd tea.Cmd
		if !s.Expanded {
			s.Expanded = true
			// Expanding changes row count; relocate the cursor to the same
			// section's header before re-rendering.
			m.refreshView()
			for j, r := range m.rows {
				if hr, ok := r.(headerRow); ok && hr.sectionIdx == h.sectionIdx {
					m.cursor = j
					break
				}
			}
		}
		if m.mode == ModeLog && !s.LogLoaded && !s.LogLoading {
			cmd = reloadSectionLog(s)
		}
		// Re-render so the cursor highlight actually moves on screen;
		// without this, expanded-section jumps update m.cursor but the
		// previously-painted row still shows the highlight.
		m.refreshViewToCursor()
		return cmd
	}
	return nil
}

// maybeAutoLoadMore checks whether the cursor is now within
// autoLoadMoreThreshold rows of the last loaded commit in its section and
// the section has more pages. Returns a tea.Cmd that fetches the next page,
// or nil if not eligible. Called from cursor-move paths only (Down/Up/PgDn/
// PgUp/Bottom and mouse click), never from refreshViewToCursor, so window
// resizes don't fire stray loads.
func (m *Model) maybeAutoLoadMore() tea.Cmd {
	c, ok := m.currentRow().(commitRow)
	if !ok {
		return nil
	}
	if c.sectionIdx < 0 || c.sectionIdx >= len(m.sections) {
		return nil
	}
	s := m.sections[c.sectionIdx]
	if !s.LogHasMore || s.LogLoading {
		return nil
	}
	if c.idx < len(s.LogCommits)-autoLoadMoreThreshold {
		return nil
	}
	s.LogLoading = true
	return loadLogCmd(s.WT.Root, len(s.LogCommits), s.LogReloadGen)
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
	if m.mode == ModeLog {
		for i, r := range m.rows {
			switch row := r.(type) {
			case commitRow:
				if z := m.zones.Get(commitZoneID(row.sectionIdx, row.idx)); z.InBounds(msg) {
					m.cursor = i
					// openCommit switches mode to ModeCommit, so no
					// auto-load-more piggyback here — clicking a near-end
					// commit drills in rather than fetching more.
					return *m, m.openCommit()
				}
			case headerRow:
				if z := m.zones.Get(headerZoneID(row.sectionIdx)); z.InBounds(msg) {
					m.cursor = i
					if row.sectionIdx >= 0 && row.sectionIdx < len(m.sections) {
						s := m.sections[row.sectionIdx]
						s.Expanded = !s.Expanded
						m.refreshView()
						var cmd tea.Cmd
						if s.Expanded && !s.LogLoaded && !s.LogLoading {
							cmd = reloadSectionLog(s)
						}
						return *m, cmd
					}
				}
			}
		}
		return *m, nil
	}
	if m.mode == ModeFileLog {
		for i := range m.commits {
			if z := m.zones.Get(commitZoneID(-1, i)); z.InBounds(msg) {
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
	m.dropResetOnModeChange()
	m.revertResetOnModeChange()
	switch m.mode {
	case ModeChanged:
		m.mode = ModeAll
		return m.reloadAllSections(true)
	case ModeAll:
		m.mode = ModeLog
		m.commitTree = nil
		m.cursor = 0
		m.diffCursor = -1
		// Active section opens by default; others stay collapsed until the
		// user expands them (Tab-jump or click-toggle), at which point
		// their first page is fetched. Active section eager-loads only if
		// it hasn't been loaded already (e.g. user toggled ModeLog before
		// in this session).
		var cmd tea.Cmd
		for i, s := range m.sections {
			if i == m.activeWT {
				s.Expanded = true
				if !s.LogLoaded && !s.LogLoading {
					cmd = reloadSectionLog(s)
				}
			} else {
				s.Expanded = false
			}
		}
		m.refreshView()
		m.positionCursorOnActiveSectionFirstCommit()
		m.ensureCursorVisible()
		return cmd
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
		cmds = append(cmds, loadStatusCmd(s.WT.Root, allMode, m.nestedChildren[s.WT.Root]))
	}
	return tea.Batch(cmds...)
}

// cycleActiveWorktree is called in ModeFileLog only — ModeLog uses
// jumpToSectionHeader to navigate between sections instead, so there's no
// "active worktree" concept inside the unified log view.
func (m *Model) cycleActiveWorktree(d int) tea.Cmd {
	if len(m.sections) <= 1 {
		return nil
	}
	m.activeWT = (m.activeWT + d + len(m.sections)) % len(m.sections)
	m.commitCursor = 0
	m.commits = nil
	m.logLoaded = false
	m.refreshView()
	if m.mode == ModeFileLog && m.fileLogPath != "" {
		return loadFileLogCmd(m.activeRoot(), m.fileLogPath)
	}
	return nil
}

func (m *Model) clearSelectedCommit() {
	m.selectedSHA = ""
	m.selectedShort = ""
	m.selectedSubject = ""
	m.selectedRoot = ""
	m.commitTree = nil
}

func (m *Model) openCommit() tea.Cmd {
	var commit git.Commit
	var root string
	switch m.mode {
	case ModeLog:
		// Route by the row's owning section, so a commit in nested-a opens
		// its own diff (not the parent repo's at the same SHA).
		c, ok := m.currentRow().(commitRow)
		if !ok || c.sectionIdx < 0 || c.sectionIdx >= len(m.sections) {
			return nil
		}
		s := m.sections[c.sectionIdx]
		if c.idx < 0 || c.idx >= len(s.LogCommits) {
			return nil
		}
		commit = s.LogCommits[c.idx]
		root = s.WT.Root
	default:
		// ModeFileLog flat list.
		if m.commitCursor < 0 || m.commitCursor >= len(m.commits) {
			return nil
		}
		commit = m.commits[m.commitCursor]
		root = m.activeRoot()
	}
	m.dropResetOnModeChange()
	m.revertResetOnModeChange()
	m.commitParent = m.mode
	m.mode = ModeCommit
	m.selectedSHA = commit.SHA
	m.selectedShort = commit.ShortSHA
	m.selectedSubject = commit.Subject
	m.selectedRoot = root
	m.commitTree = nil
	m.cursor = 0
	m.diffCursor = -1
	m.vp.SetYOffset(0)
	m.refreshView()
	return loadCommitTreeCmd(root, commit.SHA)
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
	m.dropResetOnModeChange()
	m.revertResetOnModeChange()
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
		// Manual refresh restarts fs watchers as well so newly-added
		// worktrees / nested repos gain a watcher and removed ones stop
		// firing — gdui used to require a restart for that.
		if m.restartWatchers != nil {
			m.restartWatchers()
		}
		return loadInitDataCmd(m.repoRoot, false)
	case ModeAll:
		if m.restartWatchers != nil {
			m.restartWatchers()
		}
		return loadInitDataCmd(m.repoRoot, true)
	case ModeLog:
		// Manual refresh: reload first page of every section that's been
		// touched so far. Untouched (collapsed/lazy) sections stay lazy.
		cmds := make([]tea.Cmd, 0, len(m.sections))
		for _, s := range m.sections {
			if !s.LogLoaded && !s.LogLoading {
				continue
			}
			cmds = append(cmds, reloadSectionLog(s))
		}
		return tea.Batch(cmds...)
	case ModeFileLog:
		if m.fileLogPath != "" {
			return loadFileLogCmd(m.fileLogRoot, m.fileLogPath)
		}
	case ModeCommit:
		if m.selectedSHA != "" {
			return loadCommitTreeCmd(m.selectedRoot, m.selectedSHA)
		}
	case ModeBranchDiff:
		if m.branchDiff.ref != "" {
			return loadBranchTreeCmd(m.branchDiff.root, m.branchDiff.ref)
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
// tree row (Changed/All modes); commitSHA identifies a specific commit
// when the cursor is on a commit row (Log mode). Both are empty when the
// cursor is on a section header.
type cursorAnchor struct {
	sectionRoot string
	treePath    string
	commitSHA   string
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
	case commitRow:
		if r.sectionIdx < 0 || r.sectionIdx >= len(m.sections) {
			return cursorAnchor{}
		}
		s := m.sections[r.sectionIdx]
		if r.idx < 0 || r.idx >= len(s.LogCommits) {
			return cursorAnchor{sectionRoot: s.WT.Root}
		}
		return cursorAnchor{sectionRoot: s.WT.Root, commitSHA: s.LogCommits[r.idx].SHA}
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
	if a.sectionRoot == "" && a.treePath == "" && a.commitSHA == "" {
		return
	}
	// Commit SHA anchor (Log mode). Match by section + SHA.
	if a.commitSHA != "" {
		for i, r := range m.rows {
			c, ok := r.(commitRow)
			if !ok || c.sectionIdx < 0 || c.sectionIdx >= len(m.sections) {
				continue
			}
			s := m.sections[c.sectionIdx]
			if s.WT.Root != a.sectionRoot {
				continue
			}
			if c.idx < 0 || c.idx >= len(s.LogCommits) {
				continue
			}
			if s.LogCommits[c.idx].SHA == a.commitSHA {
				m.cursor = i
				return
			}
		}
	}
	// Tree path anchor (Changed/All modes).
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
	// Fall back to the section's header row.
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
	if m.mode == ModeBranchDiff {
		return loadBranchHunksCmd(m.branchDiff.root, m.branchDiff.ref, n.Path)
	}
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

// positionCursorOnActiveSectionFirstCommit lands the cursor on the first
// commit of the active section after entering ModeLog. If commits haven't
// loaded yet, lands on the section's header so the user has a clear
// "you are here" marker while the page fetches.
func (m *Model) positionCursorOnActiveSectionFirstCommit() {
	for i, r := range m.rows {
		c, ok := r.(commitRow)
		if !ok {
			continue
		}
		if c.sectionIdx == m.activeWT {
			m.cursor = i
			return
		}
	}
	// Fallback: header (single-section repos have no header, so cursor stays 0).
	for i, r := range m.rows {
		if h, ok := r.(headerRow); ok && h.sectionIdx == m.activeWT {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
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
	filterH := 0
	if m.filter.visible() {
		filterH = 1
	}
	dropH := 0
	if m.drop.visible() {
		dropH = 1
	}
	vpH := m.height - headerH - footerH - filterH - dropH
	if vpH < 1 {
		vpH = 1
	}
	m.vp.Height = vpH
}

func (m *Model) refreshView() {
	// Always rebuild rows for tree/commit/log modes — cursor positioning
	// logic runs before the first WindowSizeMsg arrives, when m.ready is
	// still false. The viewport-dependent work below is gated separately.
	// ModeFileLog still uses the flat m.commits list and the legacy direct
	// renderer below.
	if m.mode != ModeSearch && m.mode != ModeFileLog {
		if m.mode == ModeLog {
			m.rows = flattenSectionsLogs(m.sections, len(m.sections) > 1)
		} else {
			m.rows = m.flattenForView()
		}
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
	if m.mode == ModeBranchPicker {
		m.vp.SetContent(m.renderBranchPicker())
		return
	}
	if m.mode == ModeFileLog {
		if m.commitCursor >= len(m.commits) {
			m.commitCursor = len(m.commits) - 1
		}
		if m.commitCursor < 0 {
			m.commitCursor = 0
		}
		m.vp.SetContent(m.renderLog())
		return
	}
	if m.mode == ModeLog {
		m.vp.SetContent(m.renderLogRows())
		return
	}
	m.vp.SetContent(m.renderBody())
}

// flattenForView produces the row list for the current tree-mode view —
// either commit tree or working-tree sections. Routes through the filter-
// aware helpers when m.filter has a compiled matcher; otherwise the
// matcher==nil branch in those helpers delegates back to the unfiltered
// path so non-filter renders are byte-identical to the historical UI.
func (m *Model) flattenForView() []displayRow {
	matcher := m.filter.matcher
	if m.mode == ModeCommit {
		return flattenCommitTreeFiltered(m.commitTree, matcher)
	}
	if m.mode == ModeBranchDiff {
		return flattenCommitTreeFiltered(m.branchDiff.tr, matcher)
	}
	return flattenSectionsFiltered(m.sections, m.showSectionHeaders(), matcher)
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
	if m.mode == ModeFileLog {
		m.ensureCommitCursorVisible()
		return
	}
	m.ensureCursorVisible()
}

// renderLogRows renders ModeLog by walking m.rows. Headers reuse
// renderSectionHeader (with a log-specific counts trailer); commit rows
// reuse the existing single-section format. Mouse zones are stamped per
// row so multi-section clicks route correctly.
func (m *Model) renderLogRows() string {
	if len(m.rows) == 0 {
		if m.err != nil {
			return errStyle.Render(m.err.Error())
		}
		// Determine whether any section has loaded yet — if not, show the
		// loading hint; otherwise (every section came back empty) say so.
		anyLoaded, anyLoading := false, false
		for _, s := range m.sections {
			if s.LogLoaded {
				anyLoaded = true
			}
			if s.LogLoading {
				anyLoading = true
			}
		}
		if !anyLoaded && anyLoading {
			return dimStyle.Render("  loading log…")
		}
		return dimStyle.Render("  no commits")
	}
	var b strings.Builder
	for i, r := range m.rows {
		switch row := r.(type) {
		case headerRow:
			line := m.fitWidth(m.renderLogSectionHeader(i, row.sectionIdx))
			b.WriteString(m.zones.Mark(headerZoneID(row.sectionIdx), line))
		case commitRow:
			if row.sectionIdx < 0 || row.sectionIdx >= len(m.sections) {
				continue
			}
			s := m.sections[row.sectionIdx]
			if row.idx < 0 || row.idx >= len(s.LogCommits) {
				continue
			}
			line := m.fitWidth(m.renderCommitRowAt(i, s.LogCommits[row.idx], s.WT.Root))
			b.WriteString(m.zones.Mark(commitZoneID(row.sectionIdx, row.idx), line))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderLogSectionHeader is renderSectionHeader's log-flavored cousin —
// same chevron + branch styling, but the trailer reports commit-loading
// state instead of working-tree change counts.
func (m *Model) renderLogSectionHeader(rowIdx, sectionIdx int) string {
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
	if s.Nested && s.Label != "" {
		name = s.Label + "  " + name
	}
	var trailer string
	switch {
	case s.LoadErr != nil:
		trailer = errStyle.Render("(error)")
	case !s.Expanded:
		trailer = dimStyle.Render("[collapsed]")
	case s.LogLoading && !s.LogLoaded:
		trailer = dimStyle.Render("(loading…)")
	case !s.LogLoaded:
		trailer = dimStyle.Render("(pending)")
	case len(s.LogCommits) == 0:
		trailer = dimStyle.Render("(no commits)")
	default:
		count := strconv.Itoa(len(s.LogCommits))
		if s.LogHasMore {
			count += "+"
		}
		trailer = dimStyle.Render("(" + count + " commits)")
	}
	row := fmt.Sprintf("%s %s  %s", chev, dirStyle.Render(name), trailer)
	return m.applyCursor(row, rowIdx == m.cursor)
}

// renderCommitRowAt formats a single commit row at the given m.rows index.
// Separate from renderCommitRow (which uses m.commitCursor for highlight)
// so the unified-rows path stays consistent with tree-mode rendering.
// root is the owning section's worktree root, used to look up push status.
func (m *Model) renderCommitRowAt(rowIdx int, c git.Commit, root string) string {
	mark := m.commitPushMarker(root, c.SHA)
	sha := addsStyle.Render(c.ShortSHA)
	date := dimStyle.Render(c.Date)
	author := dimStyle.Render(truncate(c.Author, 14))
	subj := fileStyle.Render(c.Subject)
	row := fmt.Sprintf("%s%s  %s  %s  %s", mark, sha, date, author, subj)
	return m.applyCursor(row, rowIdx == m.cursor)
}

// commitPushMarker returns a leading 2-column marker for a commit row: a
// warn-styled "⇡" when the commit is unpushed (present in the owning section's
// UnpushedSHAs and the repo has a remote), or two spaces otherwise so SHAs stay
// column-aligned. Returns two spaces when no section owns root.
func (m *Model) commitPushMarker(root, sha string) string {
	idx := findSectionByRoot(m.sections, root)
	if idx < 0 {
		return "  "
	}
	s := m.sections[idx]
	if s.HasRemotes && s.UnpushedSHAs[sha] {
		return warnStyle.Render("⇡") + " "
	}
	return "  "
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
	parts := []string{header, body}
	if m.filter.visible() && !m.showHelp {
		parts = append(parts, m.fitWidth(m.renderFilterStatus()))
	}
	if m.drop.visible() && !m.showHelp {
		parts = append(parts, m.fitWidth(m.renderDropStatus()))
	}
	if m.revert.visible() && !m.showHelp {
		parts = append(parts, m.fitWidth(m.renderRevertStatus()))
	}
	parts = append(parts, footer)
	return m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, parts...))
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
	if m.mode == ModeBranchPicker {
		crumb := dimStyle.Render("branch picker")
		return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", crumb)
	}
	if m.mode == ModeBranchDiff {
		crumb := dimStyle.Render(m.branchDiff.ref + "...HEAD")
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
	if m.toast != "" && time.Since(m.toastTime) < toastDuration {
		return toastStyle.Render("✓ " + m.toast)
	}
	wtCrumb := m.worktreeCrumb()
	if m.mode == ModeLog {
		var loaded, sections int
		for _, s := range m.sections {
			if s.LogLoaded {
				sections++
				loaded += len(s.LogCommits)
			}
		}
		left := fmt.Sprintf("[log] %d commits in %d sections", loaded, sections)
		return helpStyle.Render(left) + m.unpushedCrumb() +
			helpStyle.Render(" · enter open · tab next section · a toggle · ? help") +
			helpStyle.Render(m.tabHint())
	}
	if m.mode == ModeFileLog {
		left := fmt.Sprintf("[file log] %d commits · %s", len(m.commits), m.fileLogPath)
		return helpStyle.Render(left + wtCrumb + " · enter open · esc back · ? help")
	}
	if m.mode == ModeBranchPicker {
		n := len(m.branchPicker.filtered)
		left := fmt.Sprintf("[branch picker] %d / %d", n, len(m.branchPicker.all))
		return helpStyle.Render(left + " · ↑/↓ select · ⏎ diff · esc cancel · ? help")
	}

	// ModeChanged / ModeAll / ModeCommit / ModeBranchDiff all show file count + adds/dels totals.
	files, adds, dels := m.aggregateRowStats()
	totals := addsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delsStyle.Render(fmt.Sprintf("-%d", dels))
	var left, hint string
	switch m.mode {
	case ModeCommit:
		left = fmt.Sprintf("[commit %s] %d files", m.selectedShort, files)
		hint = wtCrumb + " · esc back · ? help"
	case ModeBranchDiff:
		left = fmt.Sprintf("[branch %s...HEAD] %d files", m.branchDiff.ref, files)
		hint = wtCrumb + " · esc back · ? help"
	default:
		left = fmt.Sprintf("[%s] %d changed", m.mode.String(), files)
		hint = wtCrumb + " · a toggle · ? help"
	}
	return helpStyle.Render(left+" · ") + totals + m.unpushedCrumb() + helpStyle.Render(hint)
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

// unpushedCrumb returns a warn-styled " · ⇡N unpushed" fragment summing
// UnpushedCount across sections that have a remote. Returns "" when nothing is
// unpushed or no section has a remote, so local-only / fully-pushed repos stay
// uncluttered.
func (m Model) unpushedCrumb() string {
	total := 0
	for _, s := range m.sections {
		if s.HasRemotes {
			total += s.UnpushedCount
		}
	}
	if total == 0 {
		return ""
	}
	return helpStyle.Render(" · ") + warnStyle.Render(fmt.Sprintf("⇡%d unpushed", total))
}

func (m Model) tabHint() string {
	if len(m.sections) <= 1 {
		return ""
	}
	return " · tab next worktree"
}

func (m Model) aggregateRowStats() (files, adds, dels int) {
	if m.mode == ModeCommit || m.mode == ModeBranchDiff {
		var root *tree.Node
		if m.mode == ModeCommit {
			root = m.commitTree
		} else {
			root = m.branchDiff.tr
		}
		if root != nil {
			adds, dels = root.Adds, root.Dels
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
		if m.mode == ModeBranchDiff {
			if m.branchDiff.tr == nil {
				return dimStyle.Render("  loading branch diff…")
			}
			return dimStyle.Render("  no differences vs " + m.branchDiff.ref)
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
		b.WriteString(m.zones.Mark(commitZoneID(-1, i), row))
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
	mark := m.commitPushMarker(m.logRoot, c.SHA)
	sha := addsStyle.Render(c.ShortSHA)
	date := dimStyle.Render(c.Date)
	author := dimStyle.Render(truncate(c.Author, 14))
	subj := fileStyle.Render(c.Subject)
	row := fmt.Sprintf("%s%s  %s  %s  %s", mark, sha, date, author, subj)
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
	if n.IsDir && n.ChangedFiles > 0 {
		counts = " " + dimStyle.Render(fmt.Sprintf("(%d)", n.ChangedFiles)) + counts
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
	if s.Nested && s.Label != "" {
		// Show "<rel-path>  <branch>" so two nested repos on the same branch
		// name remain distinguishable.
		name = s.Label + "  " + name
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
			{"tab / ⇧tab", "next / prev section header (worktrees & nested repos)"},
		}},
		{"Search & filter", []row{
			{"/", "open global search (paths + contents)"},
			{"f", "filter tree — substring · *glob · re:regex"},
			{"enter", "(in search) copy path; (in filter) commit query"},
			{"ctrl+u", "(in search/filter) clear query"},
			{"esc", "(in search) close; (in filter) clear and exit"},
		}},
		{"Modes", []row{
			{"a", "cycle view: changed → all → log"},
			{"B", "branch diff — pick a ref to diff <ref>...HEAD"},
			{"esc / backspace", "back out of commit, file history, or branch diff"},
		}},
		{"Drag & drop", []row{
			{"drop file", "import into repo with destination prompt"},
			{"enter", "(in drop) copy; on overwrite prompt = yes"},
			{"esc", "(in drop) skip this file; on overwrite prompt = edit"},
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

func zoneID(i int) string { return fmt.Sprintf("row-%d", i) }

// commitZoneID identifies a commit row for mouse hit-testing. In ModeLog
// the unified row list may carry commits from multiple sections, so the
// section index is part of the key; in ModeFileLog (single-section flat
// list) section is -1.
func commitZoneID(sectionIdx, idx int) string {
	return fmt.Sprintf("commit-%d-%d", sectionIdx, idx)
}
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
