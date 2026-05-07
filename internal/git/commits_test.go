package git

import "testing"

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
