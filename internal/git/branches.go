package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Branch is a ref in refs/heads/ or refs/remotes/.
type Branch struct {
	Name     string // short name: "main", "origin/feature/foo"
	FullRef  string // "refs/heads/main", "refs/remotes/origin/feature/foo"
	IsRemote bool
	IsHEAD   bool // true when this branch is the current HEAD of repoRoot
}

// ListBranches enumerates local + remote branches, excluding the symbolic
// `origin/HEAD -> origin/main` style aliases (the `--no-symbolic-target` test
// below). Local branches sort first by name, then remotes by name.
func ListBranches(repoRoot string) ([]Branch, error) {
	// %(HEAD) is '*' for the current branch, ' ' otherwise; %(symref) is
	// non-empty for symbolic refs like refs/remotes/origin/HEAD — those we drop.
	cmd := exec.Command("git", "-C", repoRoot,
		"for-each-ref",
		"--format=%(HEAD)%00%(refname)%00%(refname:short)%00%(symref)",
		"refs/heads/", "refs/remotes/")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref: %w", err)
	}
	var locals, remotes []Branch
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\x00", 4)
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "" {
			continue // symbolic alias, skip
		}
		b := Branch{
			FullRef:  fields[1],
			Name:     fields[2],
			IsRemote: strings.HasPrefix(fields[1], "refs/remotes/"),
			IsHEAD:   fields[0] == "*",
		}
		if b.IsRemote {
			remotes = append(remotes, b)
		} else {
			locals = append(locals, b)
		}
	}
	return append(locals, remotes...), nil
}

// RefDiffFiles returns the files that differ between the merge-base of `ref`
// and HEAD, and HEAD (i.e. `git diff ref...HEAD` — what HEAD has that's not on
// the common ancestor with ref). Mirrors CommitFiles' two-pass raw+numstat
// parser since the output format is identical.
func RefDiffFiles(repoRoot, ref string) ([]ChangedFile, error) {
	spec := ref + "...HEAD"
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"diff", "--raw", "--numstat", "-z", "-M", spec)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", spec, err)
	}
	return parseRawNumstat(out), nil
}

// RefDiffHunks returns the parsed unified diff for one path in
// `ref...HEAD`.
func RefDiffHunks(repoRoot, ref, path string) ([]Hunk, error) {
	spec := ref + "...HEAD"
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"diff", "-p", "-M", spec, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff -p %s -- %s: %w", spec, path, err)
	}
	return ParseUnified(out), nil
}

// parseRawNumstat parses combined `--raw --numstat -z` output. Extracted from
// CommitFiles so RefDiffFiles can reuse the same format handling.
func parseRawNumstat(out []byte) []ChangedFile {
	records := splitNUL(out)
	files := make([]ChangedFile, 0)
	idx := make(map[string]int)
	i := 0
	for ; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		if rec[0] != ':' {
			break
		}
		sp := strings.LastIndexByte(rec, ' ')
		if sp < 0 {
			continue
		}
		status := rec[sp+1:]
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
	return files
}
