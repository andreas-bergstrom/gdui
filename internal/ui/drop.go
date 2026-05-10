package ui

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// dropPhase enumerates the drop UI states. dropIdle means the prompt isn't
// drawn at all; the other two each have their own input handler.
type dropPhase int

const (
	dropIdle dropPhase = iota
	dropPromptDest
	dropPromptOverwrite
)

// dropState backs the drag-and-drop import flow. While `phase != dropIdle`,
// the bottom status row renders the prompt and `handleKeyDrop` swallows
// keystrokes. Multi-file drops accumulate in `queue`; the current source
// being prompted about is `queue[0]`.
//
// Cleared on mode switches out of tree modes — silent resume on a later
// return would be surprising.
type dropState struct {
	phase dropPhase
	queue []string
	dest  string
	err   string
}

func (d *dropState) visible() bool { return d.phase != dropIdle || d.err != "" }
func (d *dropState) active() bool  { return d.phase != dropIdle }

// dropCompletedMsg signals a successful copy. The receiving handler in
// Update places the cursor on the new file by setting a pending anchor and
// triggering a synchronous status reload.
type dropCompletedMsg struct {
	root string
	dest string
}

// dropFailedMsg signals a copy error. err is rendered in the status row;
// the queue advances so subsequent drops still get prompts.
type dropFailedMsg struct{ err error }

// handleDropPaste is the entry point from the paste-detection branch in
// handleKey. The caller has already run drop.Parse and confirmed paths is
// non-empty.
func (m *Model) handleDropPaste(paths []string) tea.Cmd {
	m.drop.queue = append(m.drop.queue, paths...)
	m.drop.err = ""
	return m.advanceDropQueue()
}

// advanceDropQueue pops the front of the queue and transitions to a fresh
// destination prompt, or returns to idle if there's nothing left. Always
// emits ClearScreen because the status row appears/disappears (same redraw-
// diff class as the filter prompt).
func (m *Model) advanceDropQueue() tea.Cmd {
	if len(m.drop.queue) == 0 {
		m.drop.phase = dropIdle
		m.drop.dest = ""
		m.refreshView()
		return tea.ClearScreen
	}
	src := m.drop.queue[0]
	m.drop.phase = dropPromptDest
	m.drop.dest = filepath.Join(m.dropDestRoot(), filepath.Base(src))
	m.refreshView()
	return tea.ClearScreen
}

// dropDestRoot picks the default destination worktree. The cursor's current
// section wins (so a drop in section 2 lands under section 2's root by
// default); falls back to activeRoot when the cursor isn't on a section row.
func (m *Model) dropDestRoot() string {
	switch r := m.currentRow().(type) {
	case treeRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return m.sections[r.sectionIdx].WT.Root
		}
	case headerRow:
		if r.sectionIdx >= 0 && r.sectionIdx < len(m.sections) {
			return m.sections[r.sectionIdx].WT.Root
		}
	}
	return m.activeRoot()
}

// dropCurrentSrc returns the source path of the in-flight drop, or "" when
// idle. Used by the renderer to show the source basename in the prompt.
func (m *Model) dropCurrentSrc() string {
	if len(m.drop.queue) == 0 {
		return ""
	}
	return m.drop.queue[0]
}

// handleKeyDrop dispatches keystrokes while the drop prompt is up. Returns
// tea.ClearScreen on any state mutation for the same redraw-diff reason as
// advanceDropQueue.
func (m *Model) handleKeyDrop(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.drop.phase {
	case dropPromptDest:
		return m.handleKeyDropDest(msg)
	case dropPromptOverwrite:
		return m.handleKeyDropOverwrite(msg)
	}
	return *m, nil
}

func (m *Model) handleKeyDropDest(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.drop.queue = m.drop.queue[1:]
		m.drop.err = ""
		return *m, m.advanceDropQueue()
	case tea.KeyEnter:
		if m.drop.dest == "" {
			return *m, nil
		}
		dest := m.drop.dest
		src := m.dropCurrentSrc()
		root := m.dropDestRootFor(dest)
		if _, err := os.Stat(dest); err == nil {
			m.drop.phase = dropPromptOverwrite
			m.refreshView()
			return *m, tea.ClearScreen
		}
		return *m, copyDropCmd(src, dest, root)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if r := []rune(m.drop.dest); len(r) > 0 {
			m.drop.dest = string(r[:len(r)-1])
			m.refreshView()
		}
		return *m, tea.ClearScreen
	case tea.KeyCtrlU:
		m.drop.dest = ""
		m.refreshView()
		return *m, tea.ClearScreen
	}
	// Append both typed runes AND pasted content into the dest field.
	// Pasting a path snippet to edit the destination is a reasonable
	// workflow; the parent paste-detection branch in handleKey already
	// short-circuited if the paste was itself a recognizable drop.
	if len(msg.Runes) > 0 {
		m.drop.dest += string(msg.Runes)
		m.refreshView()
		return *m, tea.ClearScreen
	}
	return *m, nil
}

func (m *Model) handleKeyDropOverwrite(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	confirm := func() (tea.Model, tea.Cmd) {
		dest := m.drop.dest
		src := m.dropCurrentSrc()
		root := m.dropDestRootFor(dest)
		return *m, copyDropCmd(src, dest, root)
	}
	cancel := func() (tea.Model, tea.Cmd) {
		m.drop.phase = dropPromptDest
		m.refreshView()
		return *m, tea.ClearScreen
	}
	if msg.Type == tea.KeyEnter {
		return confirm()
	}
	if msg.Type == tea.KeyEsc {
		return cancel()
	}
	// Require !msg.Paste so a clipboard containing a single "y" can't
	// accidentally confirm overwrite. Typed y/Y/n/N still works.
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

// dropDestRootFor returns the worktree root that owns dest, falling back to
// activeRoot when dest doesn't live under any section. Used at copy time so
// dropCompletedMsg knows which section to reload.
func (m *Model) dropDestRootFor(dest string) string {
	destClean := filepath.Clean(dest)
	for _, s := range m.sections {
		root := filepath.Clean(s.WT.Root)
		if destClean == root || strings.HasPrefix(destClean, root+string(filepath.Separator)) {
			return root
		}
	}
	return m.activeRoot()
}

// copyDropCmd copies src to dest atomically via a temp file in the
// destination directory + os.Rename. fsnotify watchers won't surface a
// partial file because the rename is the first moment the dest path exists.
// Reports the originating worktree root in the success message so the model
// can target the right section's reload.
func copyDropCmd(src, dest, root string) tea.Cmd {
	return func() tea.Msg {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return dropFailedMsg{err: err}
		}
		srcF, err := os.Open(src)
		if err != nil {
			return dropFailedMsg{err: err}
		}
		defer srcF.Close()
		srcInfo, err := srcF.Stat()
		if err != nil {
			return dropFailedMsg{err: err}
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".gdui-drop-*")
		if err != nil {
			return dropFailedMsg{err: err}
		}
		tmpName := tmp.Name()
		if _, err := io.Copy(tmp, srcF); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return dropFailedMsg{err: err}
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return dropFailedMsg{err: err}
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return dropFailedMsg{err: err}
		}
		// Preserve source perms but floor at 0o600 so a 0o000 source doesn't
		// produce an unreadable destination the user can't easily delete.
		perm := srcInfo.Mode().Perm()
		if perm&0o400 == 0 {
			perm |= 0o600
		}
		if err := os.Chmod(tmpName, perm); err != nil {
			os.Remove(tmpName)
			return dropFailedMsg{err: err}
		}
		if err := os.Rename(tmpName, dest); err != nil {
			os.Remove(tmpName)
			return dropFailedMsg{err: err}
		}
		return dropCompletedMsg{root: root, dest: dest}
	}
}

// expandToPath walks the tree to the directory that contains relPath and
// sets Expanded=true on each ancestor it traverses. relPath is repo-relative,
// forward-slash separated. Path-collapsed chains (where one node's Path is
// `a/b/c`) are handled correctly by matching against Path prefixes rather
// than Name segments.
//
// The leaf row (the file itself) is not toggled — only the directory chain
// leading to it.
func expandToPath(root *tree.Node, relPath string) {
	if root == nil || relPath == "" {
		return
	}
	cur := root
	for {
		var next *tree.Node
		for _, c := range cur.Children {
			if !c.IsDir {
				continue
			}
			if c.Path == relPath || strings.HasPrefix(relPath, c.Path+"/") {
				next = c
				break
			}
		}
		if next == nil {
			return
		}
		next.Expanded = true
		if next.Path == relPath {
			return
		}
		cur = next
	}
}

var (
	dropPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Bold(true)
	dropCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5")).Reverse(true)
)

// renderDropStatus draws the bottom status row. Truncation keeps the
// destination's basename intact and elides the leading directory portion —
// the basename is what the user is actively editing.
func (m *Model) renderDropStatus() string {
	switch m.drop.phase {
	case dropPromptDest:
		return m.renderDropDestRow()
	case dropPromptOverwrite:
		return m.renderDropOverwriteRow()
	}
	if m.drop.err != "" {
		left := dropPromptStyle.Render("drop: ") + errStyle.Render(m.drop.err)
		right := helpStyle.Render("esc dismiss")
		return m.padToWidth(left, right)
	}
	return ""
}

func (m *Model) renderDropDestRow() string {
	prompt := dropPromptStyle.Render("drop: ")
	srcLabel := fileStyle.Render(filepath.Base(m.dropCurrentSrc()))
	arrow := helpStyle.Render(" → ")
	destDisplay := m.truncateDest(m.drop.dest, max(8, m.width/2))
	cursor := dropCursorStyle.Render(" ")
	left := prompt + srcLabel + arrow + fileStyle.Render(destDisplay) + cursor
	right := helpStyle.Render("enter copy · esc skip")
	return m.padToWidth(left, right)
}

func (m *Model) renderDropOverwriteRow() string {
	prompt := dropPromptStyle.Render("drop: ")
	body := fileStyle.Render("overwrite ") + fileStyle.Render(filepath.Base(m.drop.dest)) + fileStyle.Render("? (y/n)")
	left := prompt + body
	right := helpStyle.Render("enter=yes · esc=edit")
	return m.padToWidth(left, right)
}

// truncateDest preserves the trailing component and replaces the leading
// directory portion with "…" when the destination overflows the budget.
// maxRunes is an upper bound. If even the basename alone overflows, we
// return as-is and let outer padding logic handle it.
func (m *Model) truncateDest(dest string, maxRunes int) string {
	r := []rune(dest)
	if len(r) <= maxRunes || maxRunes <= 0 {
		return dest
	}
	base := filepath.Base(dest)
	if len(base) >= maxRunes-1 {
		return dest
	}
	keep := maxRunes - 1 // 1 for the leading "…"
	if keep <= 0 {
		return dest
	}
	return "…" + string(r[len(r)-keep:])
}

// padToWidth joins left and right with enough spaces to right-justify right,
// or with a small gap if width information isn't available.
func (m *Model) padToWidth(left, right string) string {
	if m.width <= 0 {
		if right == "" {
			return left
		}
		return left + "  " + right
	}
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	const gap = 2
	pad := m.width - leftW - rightW
	if pad < gap {
		pad = gap
	}
	return left + strings.Repeat(" ", pad) + right
}

// dropResetOnModeChange clears the drop state when the user switches out of
// a tree mode. Silent resume on a later return would be surprising. Call
// this from the mode-cycle paths.
//
// pendingDropTarget is also cleared because it could otherwise survive a
// mode cycle and re-target the cursor on the next unrelated statusMsg.
func (m *Model) dropResetOnModeChange() {
	if m.drop.phase == dropIdle && len(m.drop.queue) == 0 && m.drop.err == "" && m.pendingDropTarget == (cursorAnchor{}) {
		return
	}
	m.drop = dropState{}
	m.pendingDropTarget = cursorAnchor{}
}

// looksLikeTruncatedDropPath reports whether s plausibly represents the
// FIRST chunk of a drop payload that got truncated by Bubble Tea's rune-
// batcher at a space character. The decisive signal: the chunk starts like
// an absolute path AND its parent directory actually exists on disk. We
// don't trust just "starts with /" because typed text could too.
func looksLikeTruncatedDropPath(s string) bool {
	if len(s) < 4 {
		return false
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	parent := filepath.Dir(s)
	if parent == "/" || parent == "." || parent == "" {
		return false
	}
	info, err := os.Stat(parent)
	if err != nil {
		return false
	}
	return info.IsDir()
}
