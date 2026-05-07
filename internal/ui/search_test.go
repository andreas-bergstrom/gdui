package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreasbergstrom/gd/internal/search"
)

// driveReady returns a Model with a ready viewport so View() doesn't bail.
func driveReady(t *testing.T) Model {
	t.Helper()
	m := New("/tmp/gd-test-not-used")
	mi, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return mi.(Model)
}

func TestSearch_EnterAndType(t *testing.T) {
	m := driveReady(t)
	// Simulate '/' to enter search.
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	if m.mode != ModeSearch {
		t.Fatalf("expected ModeSearch after '/', got %v", m.mode)
	}
	// Simulate paths arriving (bypassing actual git ls-files).
	mi, _ = m.Update(searchPathsMsg{paths: []string{"src/foo.go", "docs/bar.md"}})
	m = mi.(Model)
	if !m.search.pathsReady {
		t.Fatal("paths not marked ready")
	}
	// Type 'foo'.
	for _, r := range "foo" {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(Model)
	}
	if m.search.query != "foo" {
		t.Errorf("query: got %q, want %q", m.search.query, "foo")
	}
}

func TestSearch_BackspaceTrimsRune(t *testing.T) {
	m := driveReady(t)
	// Enter mode + load paths so kickSearch fires.
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	mi, _ = m.Update(searchPathsMsg{paths: []string{"x"}})
	m = mi.(Model)
	for _, r := range "abc" {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(Model)
	}
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = mi.(Model)
	if m.search.query != "ab" {
		t.Errorf("after backspace got %q, want %q", m.search.query, "ab")
	}
}

func TestSearch_CtrlUClearsQuery(t *testing.T) {
	m := driveReady(t)
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	mi, _ = m.Update(searchPathsMsg{paths: []string{"x"}})
	m = mi.(Model)
	for _, r := range "hello" {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(Model)
	}
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = mi.(Model)
	if m.search.query != "" {
		t.Errorf("ctrl+u should clear query, got %q", m.search.query)
	}
}

func TestSearch_EscReturnsToPrevMode(t *testing.T) {
	m := driveReady(t)
	// Start in ModeChanged (default), enter search, then Esc.
	if m.mode != ModeChanged {
		t.Fatalf("expected ModeChanged at start, got %v", m.mode)
	}
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mi.(Model)
	if m.mode != ModeChanged {
		t.Errorf("Esc should restore previous mode, got %v", m.mode)
	}
}

func TestSearch_StaleResultDropped(t *testing.T) {
	m := driveReady(t)
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	mi, _ = m.Update(searchPathsMsg{paths: []string{"x"}})
	m = mi.(Model)
	for _, r := range "abc" {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(Model)
	}
	current := m.search.pendingQ
	// Inject an out-of-date result.
	mi, _ = m.Update(searchResultMsg{
		seq:    current - 1,
		result: search.Result{Files: []string{"poison"}},
	})
	m = mi.(Model)
	for _, p := range m.search.result.Files {
		if p == "poison" {
			t.Errorf("stale result should have been dropped")
		}
	}
}

func TestSelectedText_FilesThenLines(t *testing.T) {
	m := driveReady(t)
	m.search.result = search.Result{
		Files: []string{"a.go", "b.go"},
		Lines: []search.LineMatch{
			{Path: "x.go", Line: 12, Text: "hit"},
		},
	}
	m.search.cursor = 0
	if got := m.selectedText(); got != "a.go" {
		t.Errorf("cursor 0: got %q, want a.go", got)
	}
	m.search.cursor = 1
	if got := m.selectedText(); got != "b.go" {
		t.Errorf("cursor 1: got %q, want b.go", got)
	}
	m.search.cursor = 2
	if got := m.selectedText(); got != "x.go:12" {
		t.Errorf("cursor 2: got %q, want x.go:12", got)
	}
}

func TestSelectedText_OutOfBoundsReturnsEmpty(t *testing.T) {
	m := driveReady(t)
	m.search.result = search.Result{Files: []string{"only.go"}}
	m.search.cursor = -1
	if got := m.selectedText(); got != "" {
		t.Errorf("negative cursor should return empty, got %q", got)
	}
	m.search.cursor = 99
	if got := m.selectedText(); got != "" {
		t.Errorf("oob cursor should return empty, got %q", got)
	}
}

func TestSearchTotal(t *testing.T) {
	m := driveReady(t)
	m.search.result = search.Result{
		Files: []string{"a", "b"},
		Lines: []search.LineMatch{{Path: "c", Line: 1}, {Path: "d", Line: 2}, {Path: "e", Line: 3}},
	}
	if total := m.searchTotal(); total != 5 {
		t.Errorf("searchTotal = %d, want 5", total)
	}
}

func TestHighlightQuery_PreservesText(t *testing.T) {
	// Whether ANSI styling is applied depends on the runtime color profile
	// (lipgloss disables it when no TTY is detected, which is the case in
	// `go test`). We assert only on what's logically required: every
	// character of the input is still present in some order.
	got := highlightQuery("Hello world", "world")
	if !strings.Contains(got, "world") {
		t.Errorf("'world' lost in highlight: %q", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("'Hello' lost in highlight: %q", got)
	}
}

func TestHighlightQuery_EmptyQueryPassthrough(t *testing.T) {
	if got := highlightQuery("anything", ""); got != "anything" {
		t.Errorf("empty query should be passthrough, got %q", got)
	}
}

func TestHighlightQuery_CaseInsensitive(t *testing.T) {
	// The original-case substring must survive even when the query is
	// all-lowercase and the matched text is mixed case.
	got := highlightQuery("Foo BAR baz", "bar")
	if !strings.Contains(got, "BAR") {
		t.Errorf("case-preserved match lost: %q", got)
	}
}

func TestHighlightQuery_GlobIsPassthrough(t *testing.T) {
	// A glob query has no literal substring inside paths, so the helper
	// should leave the path untouched (no useless ANSI for a missing match).
	got := highlightQuery("src/main.go", "*.go")
	if got != "src/main.go" {
		t.Errorf("glob query should be passthrough, got %q", got)
	}
}

func TestSearchCursorY_Layout(t *testing.T) {
	m := driveReady(t)
	// 2 file matches + 3 line matches.
	m.search.result = search.Result{
		Files: []string{"a", "b"},
		Lines: []search.LineMatch{{Path: "x", Line: 1}, {Path: "y", Line: 2}, {Path: "z", Line: 3}},
	}
	// Layout (y indices, 0-based):
	//   0: input
	//   1: blank
	//   2: "Files (2)" header
	//   3: a       <- cursor 0
	//   4: b       <- cursor 1
	//   5: blank
	//   6: "Matches (3)" header
	//   7: x:1     <- cursor 2
	//   8: y:2     <- cursor 3
	//   9: z:3     <- cursor 4
	cases := []struct {
		cursor int
		wantY  int
	}{
		{0, 3},
		{1, 4},
		{2, 7},
		{3, 8},
		{4, 9},
	}
	for _, c := range cases {
		m.search.cursor = c.cursor
		if got := m.searchCursorY(); got != c.wantY {
			t.Errorf("cursor=%d: searchCursorY=%d, want %d", c.cursor, got, c.wantY)
		}
	}
}

func TestSearchCursorY_FilesOnly(t *testing.T) {
	m := driveReady(t)
	m.search.result = search.Result{Files: []string{"a", "b"}}
	// Layout: 0 input, 1 blank, 2 header, 3..4 files
	m.search.cursor = 0
	if y := m.searchCursorY(); y != 3 {
		t.Errorf("cursor 0 (files-only): %d, want 3", y)
	}
	m.search.cursor = 1
	if y := m.searchCursorY(); y != 4 {
		t.Errorf("cursor 1 (files-only): %d, want 4", y)
	}
}

func TestSearchCursorY_LinesOnly(t *testing.T) {
	m := driveReady(t)
	m.search.result = search.Result{
		Lines: []search.LineMatch{{Path: "x", Line: 1}, {Path: "y", Line: 2}},
	}
	// Layout: 0 input, 1 blank, 2 header, 3..4 lines
	m.search.cursor = 0
	if y := m.searchCursorY(); y != 3 {
		t.Errorf("cursor 0 (lines-only): %d, want 3", y)
	}
	m.search.cursor = 1
	if y := m.searchCursorY(); y != 4 {
		t.Errorf("cursor 1 (lines-only): %d, want 4", y)
	}
}

func TestSearch_NavigateUpDown(t *testing.T) {
	m := driveReady(t)
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mi.(Model)
	mi, _ = m.Update(searchPathsMsg{paths: []string{"x"}})
	m = mi.(Model)
	// Inject a result with 3 file matches.
	mi, _ = m.Update(searchResultMsg{
		seq:    m.search.pendingQ,
		result: search.Result{Files: []string{"a", "b", "c"}},
	})
	m = mi.(Model)
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mi.(Model)
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mi.(Model)
	if m.search.cursor != 2 {
		t.Errorf("cursor after 2× Down: %d, want 2", m.search.cursor)
	}
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mi.(Model)
	if m.search.cursor != 1 {
		t.Errorf("cursor after Up: %d, want 1", m.search.cursor)
	}
}
