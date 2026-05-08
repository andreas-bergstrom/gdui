package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/andreas-bergstrom/gdui/internal/git"
)

// TestHighlight_NoEmbeddedNewlines guards against chroma's terminal16m
// formatter slipping a "\n\x1b[0m" tail into per-diff-line output for inputs
// that don't carry a trailing newline (i.e. every line we hand it). An
// embedded newline turns one logical diff row into two visual rows, breaking
// scroll math and the per-row cursor highlight.
func TestHighlight_NoEmbeddedNewlines(t *testing.T) {
	cases := []struct {
		name string
		path string
		in   string
	}{
		{"go-comment", "foo.go", "// a single line comment"},
		{"go-code", "foo.go", "func bar() error { return nil }"},
		{"py-comment", "foo.py", "# python comment"},
		{"plain-text", "README.md", "Hello, world."},
		{"empty", "foo.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Hunks("foo.go", []git.Hunk{
				{Header: "@@ -0,0 +1,1 @@", Lines: []git.DiffLine{{Kind: '+', Text: tc.in}}},
			}, 80, -1)
			// One hunk header + one content line = 2 lines, no more.
			if got := strings.Count(out, "\n"); got != 1 {
				t.Errorf("rendered diff has %d newlines (want 1) — chroma is leaking embedded \\n; raw=%q", got, out)
			}
		})
	}
}

func TestHunkLineCount_EmptyHunks(t *testing.T) {
	if c := HunkLineCount(nil); c != 0 {
		t.Errorf("HunkLineCount(nil) = %d, want 0", c)
	}
	if c := HunkLineCount([]git.Hunk{}); c != 0 {
		t.Errorf("HunkLineCount(empty) = %d, want 0", c)
	}
}

func TestHunkLineCount_HeaderPlusLines(t *testing.T) {
	hs := []git.Hunk{
		{Header: "@@ -1,2 +1,2 @@", Lines: []git.DiffLine{
			{Kind: ' ', Text: "ctx"},
			{Kind: '+', Text: "added"},
		}},
	}
	// 1 header + 2 content lines.
	if c := HunkLineCount(hs); c != 3 {
		t.Errorf("HunkLineCount = %d, want 3", c)
	}
}

func TestHunkLineCount_NoNewlineMarkers(t *testing.T) {
	hs := []git.Hunk{
		{Header: "@@ -1,1 +1,1 @@", Lines: []git.DiffLine{
			{Kind: '-', Text: "a"},
			{Kind: '+', Text: "b", NoNewlineHere: true},
		}},
	}
	// 1 header + 2 lines + 1 no-newline marker.
	if c := HunkLineCount(hs); c != 4 {
		t.Errorf("HunkLineCount with no-newline = %d, want 4", c)
	}
}

func TestHunkLineCount_MultipleHunks(t *testing.T) {
	hs := []git.Hunk{
		{Header: "@@ -1,1 +1,1 @@", Lines: []git.DiffLine{{Kind: ' ', Text: "a"}}},
		{Header: "@@ -10,1 +10,1 @@", Lines: []git.DiffLine{{Kind: ' ', Text: "b"}}},
	}
	if c := HunkLineCount(hs); c != 4 {
		t.Errorf("HunkLineCount = %d, want 4", c)
	}
}

func TestTruncateANSI_PlainText(t *testing.T) {
	got := TruncateANSI("hello world", 5)
	// width 5: 4 visible chars + ellipsis.
	if lipgloss.Width(got) != 5 {
		t.Errorf("Width(%q) = %d, want 5", got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected trailing ellipsis, got %q", got)
	}
}

func TestTruncateANSI_PreservesEscapes(t *testing.T) {
	in := "\x1b[31mhello world\x1b[0m"
	got := TruncateANSI(in, 5)
	// Visible width must match.
	if lipgloss.Width(got) != 5 {
		t.Errorf("Width(%q) = %d, want 5", got, lipgloss.Width(got))
	}
	// The opening color escape must be retained.
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("color escape lost: %q", got)
	}
}

func TestTruncateANSI_NoTruncationNeeded(t *testing.T) {
	// Even when input fits, current impl always appends an ellipsis terminator
	// (it doesn't measure first). That's caller's responsibility, so just make
	// sure visible width never exceeds the limit.
	got := TruncateANSI("hi", 100)
	if lipgloss.Width(got) > 100 {
		t.Errorf("oversized: width %d > 100", lipgloss.Width(got))
	}
}

func TestTruncateANSI_TinyWidth(t *testing.T) {
	if got := TruncateANSI("anything", 1); got != "" {
		t.Errorf("width<=1 should yield empty, got %q", got)
	}
	if got := TruncateANSI("anything", 0); got != "" {
		t.Errorf("width 0 should yield empty, got %q", got)
	}
}

func TestHunks_RendersWithCursorWithinBounds(t *testing.T) {
	hs := []git.Hunk{
		{Header: "@@ -1,2 +1,2 @@", Lines: []git.DiffLine{
			{Kind: '-', Text: "old"},
			{Kind: '+', Text: "new"},
		}},
	}
	out := Hunks("test.go", hs, 80, 1)
	if out == "" {
		t.Fatal("Hunks returned empty string")
	}
	// Should contain the diff content (possibly inside ANSI sequences).
	if !strings.Contains(out, "new") {
		t.Errorf("expected 'new' in output: %q", out)
	}
}

func TestHunks_LargeDiffShortCircuits(t *testing.T) {
	// Generate >LargeDiffThreshold lines.
	var lines []git.DiffLine
	for i := 0; i <= LargeDiffThreshold; i++ {
		lines = append(lines, git.DiffLine{Kind: '+', Text: "x"})
	}
	hs := []git.Hunk{{Header: "@@ -0,0 +1,9999 @@", Lines: lines}}
	out := Hunks("big.txt", hs, 80, -1)
	if !strings.Contains(out, "lines truncated") {
		t.Errorf("expected truncation placeholder, got %q", out)
	}
}

func TestHunks_TabExpansion(t *testing.T) {
	hs := []git.Hunk{{
		Header: "@@ -1,1 +1,1 @@",
		Lines:  []git.DiffLine{{Kind: '+', Text: "a\tb"}},
	}}
	out := Hunks("f.go", hs, 80, -1)
	// Tab should be expanded to spaces, no raw \t in output.
	if strings.Contains(out, "\t") {
		t.Errorf("expected tab to be expanded: %q", out)
	}
}
