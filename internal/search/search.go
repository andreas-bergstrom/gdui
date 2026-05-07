// Package search provides a fast, case-insensitive substring search over a
// repo's tracked-and-untracked file set. It returns two streams of results:
// paths whose basename/directory matches the query ("file matches"), and
// individual lines inside files that contain the query ("line matches").
//
// Designed for human-typed queries against medium repos. Files larger than
// MaxFileSize are skipped, binary files are detected via NUL-byte heuristic.
package search

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxFileSize bounds the per-file size we'll scan for line matches.
	// Above this we skip — saves the user from a hung UI on accidentally
	// committed binaries or very large generated artifacts.
	MaxFileSize = 1 << 20 // 1 MB

	// MaxFileMatches caps filename hits so a query like "go" doesn't
	// produce thousands of rows in the results pane.
	MaxFileMatches = 200

	// MaxLineMatches caps line hits across the whole repo.
	MaxLineMatches = 500

	// MaxLinesPerFile bounds how many line hits any single file can
	// contribute — prevents a single noisy file from monopolizing results.
	MaxLinesPerFile = 20

	// MaxLineLength truncates a matched line for display. The full line
	// is still considered for matching; only the rendered text is clipped.
	MaxLineLength = 240

	// binarySniffBytes is how much of a file's head we read to decide
	// "binary?" — git uses 8000, we follow.
	binarySniffBytes = 8000
)

// LineMatch is a single line within a file that contains the query.
type LineMatch struct {
	Path string // repo-relative
	Line int    // 1-indexed
	Text string // the matched line, possibly truncated for display
}

// Result holds the outcome of a single Search call.
type Result struct {
	Files     []string    // paths whose name contains the query
	Lines     []LineMatch // lines within files that contain the query
	Truncated bool        // true if any cap was hit
}

// IsGlob reports whether the query is interpreted as a shell-style filename
// glob (presence of *, ?, or [). Glob queries match filenames only; content
// search is skipped because wildcards don't have a useful meaning inside a
// line of source.
func IsGlob(q string) bool {
	return strings.ContainsAny(q, "*?[")
}

// matchGlob returns true if path matches pattern (case-insensitive). When
// the pattern has no '/', only the basename is matched — so "*.go" matches
// "src/main.go" without the user having to type "**/*.go". Otherwise the
// whole repo-relative path is matched, with '/' as a non-traversable
// separator (per filepath.Match semantics).
func matchGlob(pattern, path string) bool {
	pat := strings.ToLower(pattern)
	target := strings.ToLower(path)
	if !strings.Contains(pat, "/") {
		target = filepath.Base(target)
	}
	ok, err := filepath.Match(pat, target)
	return ok && err == nil
}

// Search walks `paths` (repo-relative; root prefix `repoRoot`) and returns
// every match for `query`. Empty query yields an empty Result. If the query
// contains glob meta-characters (see IsGlob), filenames are matched against
// it and content search is skipped.
func Search(repoRoot string, paths []string, query string) Result {
	q := strings.TrimSpace(query)
	if q == "" {
		return Result{}
	}
	glob := IsGlob(q)
	qLower := strings.ToLower(q)

	matchesPath := func(p string) bool {
		if glob {
			return matchGlob(qLower, p)
		}
		return strings.Contains(strings.ToLower(p), qLower)
	}

	var r Result

	for _, p := range paths {
		if matchesPath(p) {
			r.Files = append(r.Files, p)
			if len(r.Files) >= MaxFileMatches {
				r.Truncated = true
				break
			}
		}
	}

	// Glob queries are filename-shaped; searching for "*.go" inside file
	// contents would either be empty (literal "*") or surprising (interpreted
	// somehow). Stop here.
	if glob {
		return r
	}

	for _, p := range paths {
		if len(r.Lines) >= MaxLineMatches {
			r.Truncated = true
			break
		}
		hits, capped := searchFile(filepath.Join(repoRoot, p), p, qLower)
		if capped {
			r.Truncated = true
		}
		// If adding all hits would exceed the global cap, take only the
		// remainder so the cap is exact.
		if remaining := MaxLineMatches - len(r.Lines); remaining < len(hits) {
			hits = hits[:remaining]
			r.Truncated = true
		}
		r.Lines = append(r.Lines, hits...)
	}

	return r
}

// searchFile returns line matches for one file. `qLower` must be lower-case;
// matching is case-insensitive. The boolean indicates whether the per-file
// cap was hit.
func searchFile(full, rel, qLower string) ([]LineMatch, bool) {
	info, err := os.Stat(full)
	if err != nil || info.IsDir() || info.Size() > MaxFileSize {
		return nil, false
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	if isBinary(f) {
		return nil, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}

	var hits []LineMatch
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		if !strings.Contains(strings.ToLower(line), qLower) {
			continue
		}
		hits = append(hits, LineMatch{
			Path: rel,
			Line: lineNum,
			Text: clipLine(line),
		})
		if len(hits) >= MaxLinesPerFile {
			return hits, true
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		// On scanner errors (e.g., overlong line) we just stop scanning
		// and surface what we have. Surfacing partial matches is more
		// useful than silently dropping the file.
		return hits, false
	}
	return hits, false
}

// isBinary reads up to binarySniffBytes from r. Presence of any NUL byte in
// that prefix is treated as binary (matches git's heuristic).
func isBinary(r io.Reader) bool {
	buf := make([]byte, binarySniffBytes)
	n, _ := r.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

func clipLine(s string) string {
	if len(s) <= MaxLineLength {
		return s
	}
	return s[:MaxLineLength] + "…"
}
