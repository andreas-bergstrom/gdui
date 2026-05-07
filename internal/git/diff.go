package git

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`)

// LoadHunks fetches the unified diff for a single file and parses it into hunks.
// Handles tracked/untracked/deleted files. Returns (nil, nil) for binary files
// (caller should render a placeholder).
func LoadHunks(repoRoot string, f ChangedFile) ([]Hunk, error) {
	if f.Binary {
		return nil, nil
	}
	switch f.Kind {
	case Untracked:
		return synthesizeUntracked(repoRoot, f.Path)
	case Deleted:
		return synthesizeDeleted(repoRoot, f.Path)
	default:
		return runDiff(repoRoot, f.Path)
	}
}

func runDiff(repoRoot, path string) ([]Hunk, error) {
	// HEAD vs worktree captures both staged and unstaged.
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"diff", "--no-color", "HEAD", "--", path)
	out, err := cmd.Output()
	if err != nil {
		// Repo with no HEAD yet → fall back to comparing against empty tree.
		out2, err2 := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
			"diff", "--no-color", "--no-index", "/dev/null", filepath.Join(repoRoot, path)).Output()
		if err2 != nil {
			return nil, err
		}
		out = out2
	}
	return ParseUnified(out), nil
}

func synthesizeUntracked(repoRoot, path string) ([]Hunk, error) {
	full := filepath.Join(repoRoot, path)
	fp, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer fp.Close()
	var lines []DiffLine
	sc := bufio.NewScanner(fp)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines = append(lines, DiffLine{Kind: '+', Text: sc.Text()})
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []Hunk{{
		Header: fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines)),
		Lines:  lines,
	}}, nil
}

func synthesizeDeleted(repoRoot, path string) ([]Hunk, error) {
	out, err := exec.Command("git", "-C", repoRoot, "show", "HEAD:"+path).Output()
	if err != nil {
		return nil, err
	}
	var lines []DiffLine
	for _, t := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		lines = append(lines, DiffLine{Kind: '-', Text: t})
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []Hunk{{
		Header: fmt.Sprintf("@@ -1,%d +0,0 @@", len(lines)),
		Lines:  lines,
	}}, nil
}

// ParseUnified parses `git diff` output (a single-file or multi-file unified
// diff) into hunks. Headers (diff --git, index, ---, +++) are skipped.
// "Binary files ... differ" yields no hunks. CRLF in source is preserved.
func ParseUnified(b []byte) []Hunk {
	var hunks []Hunk
	var cur *Hunk
	lines := strings.Split(string(b), "\n")
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "diff --git"),
			strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "+++ "),
			strings.HasPrefix(ln, "old mode"),
			strings.HasPrefix(ln, "new mode"),
			strings.HasPrefix(ln, "similarity index"),
			strings.HasPrefix(ln, "rename "),
			strings.HasPrefix(ln, "copy "),
			strings.HasPrefix(ln, "deleted file"),
			strings.HasPrefix(ln, "new file"):
			cur = nil
			continue
		case strings.HasPrefix(ln, "Binary files "):
			// caller surfaces binary status separately; nothing to render
			cur = nil
			continue
		case hunkHeaderRe.MatchString(ln):
			hunks = append(hunks, Hunk{Header: ln})
			cur = &hunks[len(hunks)-1]
			continue
		case strings.HasPrefix(ln, "\\ "):
			// "\ No newline at end of file" — annotate the previous data line
			if cur != nil && len(cur.Lines) > 0 {
				cur.Lines[len(cur.Lines)-1].NoNewlineHere = true
			}
			continue
		}
		if cur == nil {
			// stray content before any hunk; ignore
			continue
		}
		if ln == "" {
			// trailing empty line from terminal newline split — skip
			continue
		}
		switch ln[0] {
		case '+', '-', ' ':
			cur.Lines = append(cur.Lines, DiffLine{Kind: ln[0], Text: ln[1:]})
		}
	}
	return hunks
}
