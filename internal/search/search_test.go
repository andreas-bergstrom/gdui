package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes the given files into a fresh tempdir and returns its path
// plus the repo-relative paths of the files written.
func fixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var paths []string
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, rel)
	}
	return root, paths
}

func TestSearch_EmptyQuery(t *testing.T) {
	root, paths := fixture(t, map[string]string{"a.go": "hello"})
	r := Search(root, paths, "")
	if len(r.Files) != 0 || len(r.Lines) != 0 {
		t.Errorf("empty query should produce no results, got %+v", r)
	}
}

func TestSearch_FilenameMatch(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"src/main.go":  "package main",
		"src/util.go":  "package main",
		"docs/note.md": "hello",
	})
	r := Search(root, paths, "main")
	// "main" is in src/main.go — both files contain "main" in content too.
	hasMain := false
	for _, p := range r.Files {
		if strings.Contains(p, "main.go") {
			hasMain = true
		}
	}
	if !hasMain {
		t.Errorf("expected main.go in file matches: %+v", r.Files)
	}
}

func TestSearch_LineMatchWithLineNumber(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"x.go": "alpha\nbeta\nGAMMA\ndelta\n",
	})
	r := Search(root, paths, "gamma")
	if len(r.Lines) != 1 {
		t.Fatalf("expected 1 line match, got %d (%+v)", len(r.Lines), r.Lines)
	}
	m := r.Lines[0]
	if m.Path != "x.go" || m.Line != 3 || m.Text != "GAMMA" {
		t.Errorf("unexpected match: %+v", m)
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	root, paths := fixture(t, map[string]string{"f.txt": "Hello WORLD"})
	for _, q := range []string{"hello", "HELLO", "Hello", "wOrLd"} {
		r := Search(root, paths, q)
		if len(r.Lines) != 1 {
			t.Errorf("query %q: got %d matches, want 1", q, len(r.Lines))
		}
	}
}

func TestSearch_BinaryFileSkipped(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"binary.bin": "AAA\x00BBB\nfoo",
		"text.txt":   "foo bar",
	})
	r := Search(root, paths, "foo")
	for _, m := range r.Lines {
		if m.Path == "binary.bin" {
			t.Errorf("binary file should not produce line matches: %+v", m)
		}
	}
	// text file should still match.
	found := false
	for _, m := range r.Lines {
		if m.Path == "text.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("text.txt should produce a match")
	}
}

func TestSearch_PerFileCap(t *testing.T) {
	// Build a file with more than MaxLinesPerFile matching lines.
	var b strings.Builder
	for i := 0; i < MaxLinesPerFile+5; i++ {
		b.WriteString("hit\n")
	}
	root, paths := fixture(t, map[string]string{"big.txt": b.String()})
	r := Search(root, paths, "hit")
	if len(r.Lines) != MaxLinesPerFile {
		t.Errorf("per-file cap not enforced: got %d, want %d", len(r.Lines), MaxLinesPerFile)
	}
	if !r.Truncated {
		t.Errorf("Truncated should be true when per-file cap hits")
	}
}

func TestSearch_MissingFileSkippedSilently(t *testing.T) {
	root := t.TempDir()
	r := Search(root, []string{"does-not-exist.go"}, "anything")
	if len(r.Lines) != 0 {
		t.Errorf("missing file should yield no errors and no matches: %+v", r)
	}
}

func TestSearch_LargeFileSkipped(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.txt")
	// A file just over MaxFileSize.
	data := strings.Repeat("x", MaxFileSize+1)
	if err := os.WriteFile(big, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(root, []string{"big.txt"}, "x")
	for _, m := range r.Lines {
		if m.Path == "big.txt" {
			t.Errorf("large file should be skipped, got match: %+v", m)
		}
	}
}

func TestSearch_LineTruncation(t *testing.T) {
	long := strings.Repeat("a", MaxLineLength+50) + "needle"
	root, paths := fixture(t, map[string]string{"x.txt": long})
	r := Search(root, paths, "needle")
	if len(r.Lines) != 1 {
		t.Fatalf("expected 1 match, got %d", len(r.Lines))
	}
	got := r.Lines[0].Text
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated line should end with ellipsis, got %q", got)
	}
	if len(got) > MaxLineLength+len("…") {
		t.Errorf("truncated text too long: %d", len(got))
	}
}

func TestSearch_NoMatchYieldsEmpty(t *testing.T) {
	root, paths := fixture(t, map[string]string{"a.go": "alpha beta"})
	r := Search(root, paths, "ZZZZZ")
	if len(r.Files) != 0 || len(r.Lines) != 0 {
		t.Errorf("expected no matches: %+v", r)
	}
}

func TestSearch_FilenameCapTruncates(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < MaxFileMatches+10; i++ {
		// Each file has the query in its name.
		files[filepath.Join("dir", "match-"+strings.Repeat("x", i)+".txt")] = "_"
	}
	root, paths := fixture(t, files)
	r := Search(root, paths, "match")
	if len(r.Files) != MaxFileMatches {
		t.Errorf("filename cap not enforced: got %d, want %d", len(r.Files), MaxFileMatches)
	}
	if !r.Truncated {
		t.Errorf("Truncated flag should be set when filename cap hits")
	}
}

func TestIsGlob(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"foo":      false,
		"foo.go":   false,
		"*.go":     true,
		"foo*":     true,
		"?est":     true,
		"[abc]":    true,
		"src/*.go": true,
	}
	for q, want := range cases {
		if got := IsGlob(q); got != want {
			t.Errorf("IsGlob(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestSearch_GlobMatchesByExtension(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"src/main.go":      "",
		"src/util.go":      "",
		"src/main_test.go": "",
		"docs/note.md":     "",
		"README.txt":       "",
	})
	r := Search(root, paths, "*.go")

	// Files should include exactly the .go files, regardless of directory.
	wantSet := map[string]bool{
		"src/main.go": true, "src/util.go": true, "src/main_test.go": true,
	}
	for _, p := range r.Files {
		if !wantSet[p] {
			t.Errorf("unexpected glob match: %q", p)
		}
		delete(wantSet, p)
	}
	for p := range wantSet {
		t.Errorf("missing expected match: %q", p)
	}

	// Glob queries should NOT produce content matches.
	if len(r.Lines) != 0 {
		t.Errorf("glob query should skip content search, got %d line matches", len(r.Lines))
	}
}

func TestSearch_GlobCaseInsensitive(t *testing.T) {
	root, paths := fixture(t, map[string]string{"Main.GO": "", "main.go": ""})
	r := Search(root, paths, "*.go")
	if len(r.Files) != 2 {
		t.Errorf("case-insensitive glob: got %d, want 2 (%v)", len(r.Files), r.Files)
	}
}

func TestSearch_GlobWithDirectoryPart(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"src/main.go":     "",
		"src/sub/dive.go": "",
		"docs/main.go":    "",
	})
	// Pattern with '/': filepath.Match semantics — '*' does NOT cross '/'.
	r := Search(root, paths, "src/*.go")

	// Should match src/main.go but NOT src/sub/dive.go (because * doesn't
	// span '/'), and NOT docs/main.go (different prefix).
	want := map[string]bool{"src/main.go": true}
	if len(r.Files) != len(want) {
		t.Fatalf("glob path: got %v, want exactly %v", r.Files, want)
	}
	for _, p := range r.Files {
		if !want[p] {
			t.Errorf("unexpected match: %q", p)
		}
	}
}

func TestSearch_QuestionMarkGlob(t *testing.T) {
	root, paths := fixture(t, map[string]string{
		"a.go": "", "ab.go": "", "abc.go": "",
	})
	// "?.go" — exactly one char before .go.
	r := Search(root, paths, "?.go")
	if len(r.Files) != 1 || r.Files[0] != "a.go" {
		t.Errorf("?.go: got %v, want [a.go]", r.Files)
	}
}

func TestSearch_GlobNoFalsePositiveOnSubstring(t *testing.T) {
	// Plain "go" is substring; "*.go" is glob. Make sure they differ:
	// substring matches "good" too, glob does not.
	root, paths := fixture(t, map[string]string{"good.txt": "", "x.go": ""})

	rSub := Search(root, paths, "go")
	rGlob := Search(root, paths, "*.go")

	if len(rSub.Files) != 2 {
		t.Errorf("substring 'go': expected both files matched, got %v", rSub.Files)
	}
	if len(rGlob.Files) != 1 || rGlob.Files[0] != "x.go" {
		t.Errorf("glob '*.go': expected only x.go, got %v", rGlob.Files)
	}
}

func TestSearch_GlobalLineCapEnforced(t *testing.T) {
	// Spread MaxLineMatches+10 hits across enough files (each file capped
	// at MaxLinesPerFile) that we exceed the global cap.
	files := map[string]string{}
	hitsNeeded := MaxLineMatches + 10
	hitsPerFile := MaxLinesPerFile
	numFiles := (hitsNeeded + hitsPerFile - 1) / hitsPerFile
	for i := 0; i < numFiles; i++ {
		files[filepath.Join("d", "f"+strings.Repeat("x", i)+".txt")] = strings.Repeat("hit\n", hitsPerFile)
	}
	root, paths := fixture(t, files)
	r := Search(root, paths, "hit")
	if len(r.Lines) > MaxLineMatches {
		t.Errorf("global line cap exceeded: got %d, want <= %d", len(r.Lines), MaxLineMatches)
	}
	if !r.Truncated {
		t.Errorf("Truncated should be set at global line cap")
	}
}
