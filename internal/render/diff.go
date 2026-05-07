package render

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreasbergstrom/gd/internal/git"
)

const LargeDiffThreshold = 2000

var (
	addMark    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")).Bold(true)
	delMark    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")).Bold(true)
	ctxMark    = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
	hunkHdr    = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Faint(true)
	noNewline  = lipgloss.NewStyle().Foreground(lipgloss.Color("#888")).Faint(true)
	cursorLine = lipgloss.NewStyle().Background(lipgloss.Color("#3b4261"))
	chromaSty  = styles.Get("github-dark")
	formatter  = formatters.Get("terminal16m")
)

// Hunks renders a slice of hunks for a given path into a multi-line string.
// Width is used for truncation/padding of background-tinted lines so the bg
// extends to the right edge. cursor is the index of the line to highlight
// (matches HunkLineCount ordering); pass -1 for no highlight.
func Hunks(path string, hunks []git.Hunk, width int, cursor int) string {
	if width < 10 {
		width = 10
	}
	total := 0
	for _, h := range hunks {
		total += len(h.Lines)
	}
	if total > LargeDiffThreshold {
		return lipgloss.NewStyle().Faint(true).Render(
			"  … " + strconv.Itoa(total) + " lines truncated; this file is too large to render inline",
		)
	}

	lex := lexers.Match(path)
	if lex == nil {
		lex = lexers.Fallback
	}
	lex = chroma.Coalesce(lex)

	var b strings.Builder
	idx := 0
	emit := func(line string) {
		if idx == cursor {
			pad := width - lipgloss.Width(line)
			if pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			line = cursorLine.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		idx++
	}
	for _, h := range hunks {
		emit(hunkHdr.Render(h.Header))
		for _, ln := range h.Lines {
			emit(renderLine(lex, ln, width))
			if ln.NoNewlineHere {
				emit(noNewline.Render(`  \ No newline at end of file`))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// HunkLineCount returns the number of rendered lines for a slice of hunks
// (hunk headers + content lines + no-newline markers). Used for both layout
// and in-diff cursor bounds.
func HunkLineCount(hunks []git.Hunk) int {
	c := 0
	for _, h := range hunks {
		c++
		c += len(h.Lines)
		for _, l := range h.Lines {
			if l.NoNewlineHere {
				c++
			}
		}
	}
	return c
}

// tabWidth controls how tabs in diff source are expanded before rendering.
// Terminals render \t as a variable number of cells, but lipgloss.Width counts
// it as 0 — so any tab in the text would silently wrap the line in the
// terminal even after we "truncate" it to viewport width. Expanding to a
// fixed-width run of spaces makes visual width == counted width.
const tabWidth = 4

func renderLine(lex chroma.Lexer, ln git.DiffLine, width int) string {
	var marker string
	switch ln.Kind {
	case '+':
		marker = addMark.Render("+")
	case '-':
		marker = delMark.Render("-")
	default:
		marker = ctxMark.Render(" ")
	}
	text := strings.ReplaceAll(ln.Text, "\t", strings.Repeat(" ", tabWidth))
	highlighted := highlight(lex, text)
	content := marker + highlighted
	visible := lipgloss.Width(content)
	if visible > width {
		return truncateANSI(content, width)
	}
	return content
}

func highlight(lex chroma.Lexer, text string) string {
	if text == "" {
		return ""
	}
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return text
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, chromaSty, it); err != nil {
		return text
	}
	// Strip trailing reset+newline that the terminal formatter sometimes adds.
	out := buf.String()
	out = strings.TrimRight(out, "\n")
	return out
}

// TruncateANSI truncates a string containing ANSI SGR escapes to `width`
// visible cells, preserving escape sequences and appending an ellipsis. Use
// this on any line about to be sent to a viewport: lipgloss soft-wraps lines
// wider than the viewport, which silently inflates the rendered line count
// and breaks cursor-position math.
func TruncateANSI(s string, width int) string { return truncateANSI(s, width) }

// truncateANSI truncates a string containing ANSI SGR escapes to `width`
// visible cells, preserving escape sequences and appending an ellipsis.
func truncateANSI(s string, width int) string {
	if width <= 1 {
		return ""
	}
	var b strings.Builder
	visible := 0
	limit := width - 1
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if visible >= limit {
			break
		}
		b.WriteByte(s[i])
		visible++
		i++
	}
	b.WriteString("\x1b[0m…")
	return b.String()
}

