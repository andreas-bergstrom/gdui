package render

import (
	"bytes"
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
	addMark   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")).Bold(true)
	delMark   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")).Bold(true)
	ctxMark   = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
	hunkHdr   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Faint(true)
	noNewline = lipgloss.NewStyle().Foreground(lipgloss.Color("#888")).Faint(true)
	chromaSty = styles.Get("github-dark")
	formatter = formatters.Get("terminal16m")
)

// Hunks renders a slice of hunks for a given path into a multi-line string.
// Width is used for truncation/padding of background-tinted lines so the bg
// extends to the right edge.
func Hunks(path string, hunks []git.Hunk, width int) string {
	if width < 10 {
		width = 10
	}
	total := 0
	for _, h := range hunks {
		total += len(h.Lines)
	}
	if total > LargeDiffThreshold {
		return lipgloss.NewStyle().Faint(true).Render(
			"  … " + itoa(total) + " lines truncated; this file is too large to render inline",
		)
	}

	lex := lexers.Match(path)
	if lex == nil {
		lex = lexers.Fallback
	}
	lex = chroma.Coalesce(lex)

	var b strings.Builder
	for _, h := range hunks {
		b.WriteString(hunkHdr.Render(h.Header))
		b.WriteByte('\n')
		for _, ln := range h.Lines {
			b.WriteString(renderLine(lex, ln, width))
			b.WriteByte('\n')
			if ln.NoNewlineHere {
				b.WriteString(noNewline.Render(`  \ No newline at end of file`))
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

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
	highlighted := highlight(lex, ln.Text)
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
