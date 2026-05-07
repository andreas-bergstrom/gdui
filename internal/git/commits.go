package git

import (
	"os/exec"
	"strconv"
	"strings"
)

// Commit is a single entry in `git log`.
type Commit struct {
	SHA      string
	ShortSHA string
	Author   string
	Date     string // YYYY-MM-DD
	Subject  string
}

// Log returns up to `limit` most recent commits on the current branch.
func Log(repoRoot string, limit int) ([]Commit, error) {
	cmd := exec.Command("git", "-C", repoRoot,
		"log",
		"-n", strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%ad%x1f%s",
		"--date=short",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	var commits []Commit
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(line, "\x1f", 5)
		if len(parts) < 5 {
			continue
		}
		commits = append(commits, Commit{
			SHA:      parts[0],
			ShortSHA: parts[1],
			Author:   parts[2],
			Date:     parts[3],
			Subject:  parts[4],
		})
	}
	return commits, nil
}

// CommitFiles returns the files changed in a single commit, with line counts
// and rename info.
//
// Uses `git diff-tree` (not `git show`) so we get sensible output across all
// commit shapes: `--root` makes root commits diff against /dev/null (showing
// all files as adds); `-m --first-parent` makes merge commits emit a single
// diff against their first parent rather than the empty default. For ordinary
// commits, both flags are harmless no-ops.
//
// The single invocation yields both the raw section (lines starting with ':',
// gives Kind / OldPath) and the numstat section (gives Adds/Dels/Binary).
// Records are NUL-separated; the two sections have distinct record shapes so
// we can tell them apart inline.
func CommitFiles(repoRoot, sha string) ([]ChangedFile, error) {
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"diff-tree", "-r", "--no-commit-id", "--root", "-m", "--first-parent",
		"--raw", "--numstat", "-z", "-M", sha)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	records := splitNUL(out)

	// Pass 1: raw section — every record begins with ':' until we hit a
	// numstat-shaped record. Establishes Kind, OldPath, and entry order.
	files := make([]ChangedFile, 0)
	idx := make(map[string]int)
	i := 0
	for ; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		if rec[0] != ':' {
			break // start of numstat section
		}
		// `:mode mode sha sha STATUS\0path[\0newpath]`
		tab := strings.LastIndexByte(rec, ' ')
		if tab < 0 {
			continue
		}
		status := rec[tab+1:]
		if status == "" || i+1 >= len(records) {
			continue
		}
		i++
		path := records[i]
		var oldPath string
		if status[0] == 'R' || status[0] == 'C' {
			if i+1 >= len(records) {
				continue
			}
			oldPath = path
			i++
			path = records[i]
		}
		idx[path] = len(files)
		files = append(files, ChangedFile{
			Path:    path,
			OldPath: oldPath,
			Kind:    kindFromStatusLetter(status[0]),
		})
	}

	// Pass 2: numstat section — fill Adds/Dels/Binary by path.
	for ; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		a, d, path := fields[0], fields[1], fields[2]
		if path == "" {
			// Rename: numstat emits empty path field then oldpath, newpath.
			if i+2 >= len(records) {
				break
			}
			path = records[i+2]
			i += 2
		}
		j, ok := idx[path]
		if !ok {
			continue
		}
		if a == "-" || d == "-" {
			files[j].Binary = true
			continue
		}
		ai, _ := strconv.Atoi(a)
		di, _ := strconv.Atoi(d)
		files[j].Adds, files[j].Dels = ai, di
	}
	return files, nil
}

func kindFromStatusLetter(c byte) ChangeKind {
	switch c {
	case 'A':
		return Added
	case 'D':
		return Deleted
	case 'R', 'C':
		return Renamed
	default:
		return Modified
	}
}

// CommitHunks returns the parsed unified diff for one path inside a commit.
// Uses `diff-tree --root -m --first-parent` for the same root/merge handling
// as CommitFiles.
func CommitHunks(repoRoot, sha, path string) ([]Hunk, error) {
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"diff-tree", "-p", "--no-commit-id", "--root", "-m", "--first-parent",
		sha, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseUnified(out), nil
}
