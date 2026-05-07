package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreasbergstrom/gd/internal/clipboard"
	"github.com/andreasbergstrom/gd/internal/git"
	"github.com/andreasbergstrom/gd/internal/search"
)

// toastDuration is how long the "copied …" footer hint sticks before
// reverting to the standard hints. Short enough not to annoy, long enough
// to read.
const toastDuration = 1500 * time.Millisecond

// searchState holds everything that's specific to ModeSearch.
type searchState struct {
	prevMode viewMode

	query  string
	cursor int // unified index across files+lines

	// Cached repo path list. Loaded lazily on first entry; reused for
	// subsequent entries within the session. Cleared on RefreshMsg outside
	// search mode (see Update) so it picks up new files.
	paths      []string
	pathsErr   error
	pathsReady bool

	// Latest search result, plus the sequence of the query that produced it.
	// We tag every dispatched search with a sequence so out-of-order results
	// (cheap searches racing) can't replace newer ones with older ones.
	result    search.Result
	resultSeq int
	pendingQ  int // current in-flight query sequence

	toast      string
	toastSeq   int
	toastUntil time.Time
}

// --- messages ---

type searchPathsMsg struct {
	paths []string
	err   error
}

type searchResultMsg struct {
	seq    int
	result search.Result
}

type clipboardCopiedMsg struct {
	text string
	err  error
	seq  int
}

type clipboardToastExpiredMsg struct {
	seq int
}

// --- commands ---

func loadSearchPathsCmd(repoRoot string) tea.Cmd {
	return func() tea.Msg {
		paths, err := git.ListAll(repoRoot)
		return searchPathsMsg{paths: paths, err: err}
	}
}

func runSearchCmd(repoRoot string, paths []string, query string, seq int) tea.Cmd {
	return func() tea.Msg {
		return searchResultMsg{seq: seq, result: search.Search(repoRoot, paths, query)}
	}
}

func copyToClipboardCmd(text string, seq int) tea.Cmd {
	return func() tea.Msg {
		err := clipboard.Copy(text)
		return clipboardCopiedMsg{text: text, err: err, seq: seq}
	}
}

func toastExpireCmd(seq int) tea.Cmd {
	return tea.Tick(toastDuration, func(time.Time) tea.Msg {
		return clipboardToastExpiredMsg{seq: seq}
	})
}

// --- entry / exit ---

func (m *Model) enterSearch() tea.Cmd {
	if m.mode == ModeSearch {
		return nil
	}
	m.search.prevMode = m.mode
	m.mode = ModeSearch
	m.search.cursor = 0
	m.vp.SetYOffset(0)
	m.refreshView()
	if m.search.pathsReady {
		// Re-search with whatever query is already in the field (typically empty).
		return m.kickSearch()
	}
	return loadSearchPathsCmd(m.repoRoot)
}

func (m *Model) exitSearch() tea.Cmd {
	prev := m.search.prevMode
	if prev == ModeSearch {
		prev = ModeChanged // safety net
	}
	m.mode = prev
	m.vp.SetYOffset(0)
	m.refreshView()
	return nil
}

// kickSearch returns a Cmd that runs the current query against cached paths.
// Bumps the sequence so any in-flight stale searches get ignored when they
// resolve.
func (m *Model) kickSearch() tea.Cmd {
	if !m.search.pathsReady {
		return nil
	}
	m.search.pendingQ++
	seq := m.search.pendingQ
	q := m.search.query
	paths := m.search.paths
	return runSearchCmd(m.repoRoot, paths, q, seq)
}

// --- key handling ---

func (m *Model) handleKeySearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		return *m, m.exitSearch()
	case tea.KeyEnter:
		return *m, m.copySelected()
	case tea.KeyBackspace, tea.KeyCtrlH:
		if len(m.search.query) > 0 {
			// Drop one rune (UTF-8 safe).
			r := []rune(m.search.query)
			m.search.query = string(r[:len(r)-1])
			m.search.cursor = 0
			m.refreshView()
			return *m, m.kickSearch()
		}
		return *m, nil
	case tea.KeyCtrlU:
		if len(m.search.query) > 0 {
			m.search.query = ""
			m.search.cursor = 0
			m.refreshView()
			return *m, m.kickSearch()
		}
		return *m, nil
	case tea.KeyUp:
		m.moveSearchCursor(-1)
		return *m, nil
	case tea.KeyDown:
		m.moveSearchCursor(1)
		return *m, nil
	case tea.KeyPgUp:
		m.moveSearchCursor(-m.vp.Height / 2)
		return *m, nil
	case tea.KeyPgDown:
		m.moveSearchCursor(m.vp.Height / 2)
		return *m, nil
	case tea.KeyHome:
		m.search.cursor = 0
		m.refreshView()
		return *m, nil
	case tea.KeyEnd:
		m.search.cursor = m.searchTotal() - 1
		if m.search.cursor < 0 {
			m.search.cursor = 0
		}
		m.refreshView()
		return *m, nil
	}
	// Printable rune(s) — append to query.
	if len(msg.Runes) > 0 {
		m.search.query += string(msg.Runes)
		m.search.cursor = 0
		m.refreshView()
		return *m, m.kickSearch()
	}
	return *m, nil
}

func (m *Model) moveSearchCursor(d int) {
	total := m.searchTotal()
	if total <= 0 {
		m.search.cursor = 0
		return
	}
	m.search.cursor += d
	if m.search.cursor < 0 {
		m.search.cursor = 0
	}
	if m.search.cursor >= total {
		m.search.cursor = total - 1
	}
	m.refreshView()
	m.scrollTo(m.searchCursorY())
}

// searchCursorY returns the rendered Y line index of the currently selected
// search result. Mirrors the layout in renderSearch — input row, blank,
// section header(s), match rows, optional blank between the two sections.
func (m *Model) searchCursorY() int {
	nFiles := len(m.search.result.Files)
	nLines := len(m.search.result.Lines)
	cursor := m.search.cursor

	y := 2 // input + blank
	if nFiles > 0 {
		y++ // "Files (N)" header
		if cursor < nFiles {
			return y + cursor
		}
		y += nFiles
		y++ // blank between sections
		cursor -= nFiles
	}
	if nLines > 0 {
		y++ // "Matches (M)" header
		if cursor >= 0 && cursor < nLines {
			return y + cursor
		}
	}
	return y
}

func (m *Model) searchTotal() int {
	return len(m.search.result.Files) + len(m.search.result.Lines)
}

// copySelected grabs the path (or path:line) for the currently highlighted
// result and copies it via clipboard. Bumps the toast sequence so an earlier
// in-flight expiration timer can't clear our fresh toast.
func (m *Model) copySelected() tea.Cmd {
	text := m.selectedText()
	if text == "" {
		return nil
	}
	m.search.toastSeq++
	seq := m.search.toastSeq
	m.search.toast = "copied: " + text
	m.search.toastUntil = time.Now().Add(toastDuration)
	m.refreshView()
	return tea.Batch(copyToClipboardCmd(text, seq), toastExpireCmd(seq))
}

func (m *Model) selectedText() string {
	idx := m.search.cursor
	if idx < 0 {
		return ""
	}
	files := m.search.result.Files
	if idx < len(files) {
		return files[idx]
	}
	idx -= len(files)
	lines := m.search.result.Lines
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return fmt.Sprintf("%s:%d", lines[idx].Path, lines[idx].Line)
}

// --- rendering ---

var (
	searchPromptStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Bold(true)
	searchSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#bb9af7")).Bold(true)
	searchHitStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68")).Bold(true)
)

func (m *Model) renderSearch() string {
	var b strings.Builder

	b.WriteString(m.fitWidth(m.searchInputLine()))
	b.WriteByte('\n')
	b.WriteByte('\n')

	if !m.search.pathsReady {
		if m.search.pathsErr != nil {
			b.WriteString(errStyle.Render("  index error: " + m.search.pathsErr.Error()))
		} else {
			b.WriteString(dimStyle.Render("  indexing repository…"))
		}
		return b.String()
	}

	if m.search.query == "" {
		b.WriteString(dimStyle.Render("  type to search file paths and contents · use *.go for globs"))
		return b.String()
	}

	files := m.search.result.Files
	lines := m.search.result.Lines

	if len(files) == 0 && len(lines) == 0 {
		b.WriteString(dimStyle.Render("  no matches"))
		return b.String()
	}

	idx := 0

	if len(files) > 0 {
		b.WriteString(m.fitWidth(searchSectionStyle.Render(fmt.Sprintf("  Files (%d)", len(files)))))
		b.WriteByte('\n')
		for _, p := range files {
			row := m.renderSearchFileRow(idx, p)
			b.WriteString(m.zones.Mark(searchZoneID(idx), m.fitWidth(row)))
			b.WriteByte('\n')
			idx++
		}
		b.WriteByte('\n')
	}

	if len(lines) > 0 {
		b.WriteString(m.fitWidth(searchSectionStyle.Render(fmt.Sprintf("  Matches (%d)", len(lines)))))
		b.WriteByte('\n')
		for _, ln := range lines {
			row := m.renderSearchLineRow(idx, ln)
			b.WriteString(m.zones.Mark(searchZoneID(idx), m.fitLines(row)))
			b.WriteByte('\n')
			idx++
		}
	}

	if m.search.result.Truncated {
		b.WriteString(dimStyle.Render("  …results truncated"))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) searchInputLine() string {
	prompt := searchPromptStyle.Render(" / ")
	q := m.search.query
	cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
	return prompt + fileStyle.Render(q) + cursor
}

func (m *Model) renderSearchFileRow(rowIdx int, path string) string {
	row := "  " + highlightQuery(path, m.search.query)
	return m.applyCursor(row, rowIdx == m.search.cursor)
}

func (m *Model) renderSearchLineRow(rowIdx int, ln search.LineMatch) string {
	loc := dimStyle.Render(fmt.Sprintf("%s:%d", ln.Path, ln.Line))
	body := highlightQuery(ln.Text, m.search.query)
	row := "  " + loc + "  " + fileStyle.Render(body)
	return m.applyCursor(row, rowIdx == m.search.cursor)
}

// highlightQuery wraps every (case-insensitive) occurrence of `q` inside `s`
// with the search-hit style. Returns s unchanged if q is empty or is a glob
// pattern (highlighting a literal "*" or "?" gives nothing useful).
func highlightQuery(s, q string) string {
	if q == "" || search.IsGlob(q) {
		return s
	}
	var b strings.Builder
	low := strings.ToLower(s)
	qLow := strings.ToLower(q)
	i := 0
	for i < len(low) {
		hit := strings.Index(low[i:], qLow)
		if hit < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+hit])
		end := i + hit + len(q)
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(searchHitStyle.Render(s[i+hit : end]))
		i = end
	}
	return b.String()
}

// searchFooter overrides the default footer when in ModeSearch.
func (m *Model) searchFooter() string {
	// While the toast is fresh, show that instead of the hints.
	if m.search.toast != "" && time.Now().Before(m.search.toastUntil) {
		return addsStyle.Render(m.search.toast)
	}
	files := len(m.search.result.Files)
	lines := len(m.search.result.Lines)
	left := fmt.Sprintf("[search] %d files · %d matches", files, lines)
	hint := " · ↑↓ navigate · enter copy · esc back · ? help"
	return helpStyle.Render(left + hint)
}

func searchZoneID(i int) string { return fmt.Sprintf("search-%d", i) }

// handleSearchMouse maps a left-click in ModeSearch onto the corresponding
// result row, then triggers a copy. Wheel events fall through to the existing
// viewport scroll handler.
func (m *Model) handleSearchMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
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
	for i := 0; i < m.searchTotal(); i++ {
		if z := m.zones.Get(searchZoneID(i)); z.InBounds(msg) {
			m.search.cursor = i
			return *m, m.copySelected()
		}
	}
	return *m, nil
}
