package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func zeroPad(i int) string { return fmt.Sprintf("%02d", i) }

func TestParseLog_Empty(t *testing.T) {
	if got := parseLog(nil); got != nil {
		t.Errorf("parseLog(nil) = %v, want nil", got)
	}
	if got := parseLog([]byte("")); got != nil {
		t.Errorf("parseLog(empty) = %v, want nil", got)
	}
	if got := parseLog([]byte("\n\n")); got != nil {
		t.Errorf("parseLog(whitespace) = %v, want nil", got)
	}
}

func TestParseLog_SingleCommit(t *testing.T) {
	in := []byte("abc123def456\x1fabc123d\x1fAlice\x1f2026-05-07\x1fAdd feature\n")
	got := parseLog(in)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	c := got[0]
	if c.SHA != "abc123def456" || c.ShortSHA != "abc123d" || c.Author != "Alice" || c.Date != "2026-05-07" || c.Subject != "Add feature" {
		t.Errorf("commit fields wrong: %+v", c)
	}
}

func TestParseLog_MultipleCommits(t *testing.T) {
	in := []byte(
		"sha1\x1fs1\x1fAlice\x1f2026-05-07\x1ffirst\n" +
			"sha2\x1fs2\x1fBob\x1f2026-05-06\x1fsecond")
	got := parseLog(in)
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2", len(got))
	}
	if got[0].Subject != "first" || got[1].Subject != "second" {
		t.Errorf("subjects: %q, %q", got[0].Subject, got[1].Subject)
	}
}

func TestParseLog_SkipsMalformedLines(t *testing.T) {
	in := []byte(
		"sha1\x1fs1\x1fAlice\x1f2026-05-07\x1fok\n" +
			"not enough fields\n" +
			"sha2\x1fs2\x1fBob\x1f2026-05-06\x1fok2")
	got := parseLog(in)
	if len(got) != 2 {
		t.Fatalf("malformed line should be skipped; got %d commits", len(got))
	}
}

func TestParseLog_SubjectWithUnitSeparatorIsSafe(t *testing.T) {
	// SplitN with limit 5 stops splitting after the 4th separator, so any
	// (unlikely) unit separator inside the subject gets glued back into the
	// subject field rather than corrupting structure.
	in := []byte("sha\x1fs\x1fA\x1f2026-05-07\x1fweird\x1fsubject")
	got := parseLog(in)
	if len(got) != 1 || got[0].Subject != "weird\x1fsubject" {
		t.Errorf("got %+v", got)
	}
}

func TestKindFromStatusLetter(t *testing.T) {
	cases := map[byte]ChangeKind{
		'A': Added,
		'D': Deleted,
		'R': Renamed,
		'C': Renamed,
		'M': Modified,
		'X': Modified, // unknown defaults to Modified
	}
	for letter, want := range cases {
		if got := kindFromStatusLetter(letter); got != want {
			t.Errorf("kindFromStatusLetter(%c) = %v, want %v", letter, got, want)
		}
	}
}

func TestLog_SkipReturnsOlderCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not in PATH: %v", err)
	}
	root := resolveTempRoot(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	mustRun := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	mustRun("init", "-q", "-b", "main")
	// 15 commits, each with subject c01..c15 (oldest c01, newest c15).
	for i := 1; i <= 15; i++ {
		name := "c" + zeroPad(i)
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun("add", name)
		mustRun("commit", "-q", "-m", name)
	}

	// Page 1: 10 newest. Expect c15 first, c06 last.
	page1, err := Log(repo, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 10 {
		t.Fatalf("page1: got %d commits, want 10", len(page1))
	}
	if page1[0].Subject != "c15" || page1[9].Subject != "c06" {
		t.Errorf("page1 boundary: first=%q last=%q (want c15 / c06)", page1[0].Subject, page1[9].Subject)
	}

	// Page 2 with skip=10: 5 oldest. Expect c05 first, c01 last.
	page2, err := Log(repo, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2: got %d commits, want 5", len(page2))
	}
	if page2[0].Subject != "c05" || page2[4].Subject != "c01" {
		t.Errorf("page2 boundary: first=%q last=%q (want c05 / c01)", page2[0].Subject, page2[4].Subject)
	}

	// Skip past everything: empty result, no error.
	none, err := Log(repo, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("skip=100 should return empty, got %d", len(none))
	}
}

func TestKindFromXY(t *testing.T) {
	cases := map[string]ChangeKind{
		"A.": Added,
		".A": Added,
		"D.": Deleted,
		".D": Deleted,
		"R.": Renamed,
		"M.": Modified,
		".M": Modified,
		"":   Modified, // length<2 defaults
		"X":  Modified,
	}
	for xy, want := range cases {
		if got := kindFromXY(xy); got != want {
			t.Errorf("kindFromXY(%q) = %v, want %v", xy, got, want)
		}
	}
}
