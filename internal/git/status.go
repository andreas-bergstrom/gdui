package git

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// maxScannerLine bounds bufio.Scanner buffers used to count lines in arbitrary
// files. Files with single lines longer than this will yield a truncated count
// rather than failing the whole status load.
const maxScannerLine = 4 * 1024 * 1024

// Status returns the list of changed files in the working tree relative to HEAD,
// merging staged + unstaged + untracked, with line counts.
func Status(repoRoot string) ([]ChangedFile, error) {
	files, err := porcelainV2(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	if err := fillNumstat(repoRoot, files); err != nil {
		return nil, err
	}
	if err := fillUntracked(repoRoot, files); err != nil {
		return nil, err
	}
	return files, nil
}

func porcelainV2(repoRoot string) ([]ChangedFile, error) {
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"status", "--porcelain=v2", "-z", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePorcelainV2(out), nil
}

// parsePorcelainV2 parses NUL-separated `git status --porcelain=v2 -z` output.
func parsePorcelainV2(out []byte) []ChangedFile {
	var files []ChangedFile
	records := splitNUL(out)
	i := 0
	for i < len(records) {
		rec := records[i]
		if rec == "" {
			i++
			continue
		}
		switch rec[0] {
		case '1':
			// 1 XY sub mH mI mW hH hI path
			f := parseOrdinary(rec)
			if f != nil {
				files = append(files, *f)
			}
			i++
		case '2':
			// 2 XY sub mH mI mW hH hI Xscore path  (next record is origPath)
			f := parseRename(rec)
			if i+1 < len(records) && f != nil {
				f.OldPath = records[i+1]
			}
			if f != nil {
				files = append(files, *f)
			}
			i += 2
		case '?':
			// ? path
			path := strings.TrimSpace(strings.TrimPrefix(rec, "?"))
			// A trailing slash marks a nested repo or linked worktree git
			// won't descend into (even with --untracked-files=all). It's
			// not a file; nested repos get their own section instead.
			if path != "" && !strings.HasSuffix(path, "/") {
				files = append(files, ChangedFile{Path: path, Kind: Untracked})
			}
			i++
		default:
			i++
		}
	}
	return files
}

func parseOrdinary(rec string) *ChangedFile {
	// "1 XY sub mH mI mW hH hI path"
	parts := strings.SplitN(rec, " ", 9)
	if len(parts) < 9 {
		return nil
	}
	xy := parts[1]
	path := parts[8]
	return &ChangedFile{Path: path, Kind: kindFromXY(xy)}
}

func parseRename(rec string) *ChangedFile {
	// "2 XY sub mH mI mW hH hI Xscore path"
	parts := strings.SplitN(rec, " ", 10)
	if len(parts) < 10 {
		return nil
	}
	xy := parts[1]
	path := parts[9]
	k := Renamed
	// fall back: respect deletion if XY indicates so
	if strings.ContainsAny(xy, "D") && !strings.ContainsAny(xy, "R") {
		k = Deleted
	}
	return &ChangedFile{Path: path, Kind: k}
}

func kindFromXY(xy string) ChangeKind {
	if len(xy) < 2 {
		return Modified
	}
	x, y := xy[0], xy[1]
	switch {
	case x == 'A' || y == 'A':
		return Added
	case x == 'D' || y == 'D':
		return Deleted
	case x == 'R' || y == 'R':
		return Renamed
	default:
		return Modified
	}
}

// fillNumstat sums staged + unstaged numstat lines and assigns them to files.
func fillNumstat(repoRoot string, files []ChangedFile) error {
	totals := map[string][2]int{} // path -> {adds, dels}
	binary := map[string]bool{}

	gather := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", repoRoot, "-c", "core.quotepath=false", "diff", "--numstat", "-z"}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		// numstat -z format: "adds\tdels\tpath\0" but renames look like "adds\tdels\t\0old\0new\0"
		records := splitNUL(out)
		for i := 0; i < len(records); i++ {
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
				// rename: skip the trailing oldpath, then take newpath
				if i+2 < len(records) {
					path = records[i+2]
					i += 2
				} else {
					continue
				}
			}
			isBinary := a == "-" || d == "-"
			if isBinary {
				binary[path] = true
				continue
			}
			ai, _ := strconv.Atoi(a)
			di, _ := strconv.Atoi(d)
			cur := totals[path]
			totals[path] = [2]int{cur[0] + ai, cur[1] + di}
		}
		return nil
	}

	// HEAD vs working tree (staged + unstaged combined)
	if err := gather("HEAD"); err != nil {
		// no HEAD yet (fresh repo) is fine — treat tracked as nothing
		_ = err
	}

	for i := range files {
		f := &files[i]
		if t, ok := totals[f.Path]; ok {
			f.Adds, f.Dels = t[0], t[1]
		}
		if binary[f.Path] {
			f.Binary = true
		}
	}
	return nil
}

// fillUntracked counts lines in untracked files as adds.
func fillUntracked(repoRoot string, files []ChangedFile) error {
	for i := range files {
		f := &files[i]
		if f.Kind != Untracked {
			continue
		}
		full := filepath.Join(repoRoot, f.Path)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		fp, err := os.Open(full)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fp)
		sc.Buffer(make([]byte, 64*1024), maxScannerLine)
		n := 0
		for sc.Scan() {
			n++
		}
		fp.Close()
		f.Adds = n
	}
	return nil
}

func splitNUL(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	// Strip trailing empty
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
