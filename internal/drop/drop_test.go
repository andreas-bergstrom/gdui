package drop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixture writes a sentinel file at <tmpDir>/<rel> and returns the absolute
// path. rel may contain "/" — intermediate dirs are created.
func fixture(t *testing.T, dir, rel string) string {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %q: %v", abs, err)
	}
	return abs
}

// fixtureDir creates an empty directory and returns its absolute path.
func fixtureDir(t *testing.T, dir, rel string) string {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return abs
}

func TestParse_PlainPath(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo.txt")
	got := Parse(p)
	if len(got) != 1 || got[0] != p {
		t.Errorf("plain path: want [%q], got %v", p, got)
	}
}

func TestParse_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo.txt")
	got := Parse(p + "\n")
	if len(got) != 1 || got[0] != p {
		t.Errorf("trailing newline: want [%q], got %v", p, got)
	}
	got = Parse(p + "\r\n")
	if len(got) != 1 || got[0] != p {
		t.Errorf("trailing CRLF: want [%q], got %v", p, got)
	}
}

func TestParse_BackslashEscapedSpace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash escape is a Unix-only convention")
	}
	dir := t.TempDir()
	p := fixture(t, dir, "with space.txt")
	// Build "<dir>/with\ space.txt" by re-escaping spaces in the absolute path.
	payload := strings.ReplaceAll(p, " ", `\ `)
	got := Parse(payload)
	if len(got) != 1 || got[0] != p {
		t.Errorf("backslash-escaped space: want [%q], got %v (payload=%q)", p, got, payload)
	}
}

func TestParse_SingleQuoted(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo bar.txt")
	got := Parse("'" + p + "'")
	if len(got) != 1 || got[0] != p {
		t.Errorf("single-quoted: want [%q], got %v", p, got)
	}
}

func TestParse_DoubleQuoted(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo.txt")
	got := Parse(`"` + p + `"`)
	if len(got) != 1 || got[0] != p {
		t.Errorf("double-quoted: want [%q], got %v", p, got)
	}
}

func TestParse_FileURI(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo bar.txt")
	// URL-encode the space; rest of the path is ASCII-safe.
	encoded := strings.ReplaceAll(p, " ", "%20")
	got := Parse("file://" + encoded)
	if len(got) != 1 || got[0] != p {
		t.Errorf("file:// URI: want [%q], got %v (payload=%q)", p, got, "file://"+encoded)
	}
}

func TestParse_MultipleSingleQuoted(t *testing.T) {
	dir := t.TempDir()
	a := fixture(t, dir, "a.txt")
	b := fixture(t, dir, "b.txt")
	payload := "'" + a + "' '" + b + "'"
	got := Parse(payload)
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("multi single-quoted: want [%q %q], got %v", a, b, got)
	}
}

func TestParse_MultipleNewlineSeparated(t *testing.T) {
	dir := t.TempDir()
	a := fixture(t, dir, "a.txt")
	b := fixture(t, dir, "b.txt")
	got := Parse(a + "\n" + b)
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("newline-separated: want [%q %q], got %v", a, b, got)
	}
}

func TestParse_RealTextPasteReturnsNil(t *testing.T) {
	got := Parse("hello world this is not a path")
	if got != nil {
		t.Errorf("text paste should return nil, got %v", got)
	}
}

func TestParse_OneFakePathInBatchRejectsAll(t *testing.T) {
	dir := t.TempDir()
	a := fixture(t, dir, "real.txt")
	missing := filepath.Join(dir, "missing.txt")
	got := Parse(a + " " + missing)
	if got != nil {
		t.Errorf("strict rule: any fake path should reject whole batch; got %v", got)
	}
}

func TestParse_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	subdir := fixtureDir(t, dir, "somedir")
	got := Parse(subdir)
	if got != nil {
		t.Errorf("directory should be rejected (file-only mode), got %v", got)
	}
}

func TestParse_DirectoryInBatchRejectsAll(t *testing.T) {
	dir := t.TempDir()
	a := fixture(t, dir, "a.txt")
	subdir := fixtureDir(t, dir, "somedir")
	got := Parse(a + " " + subdir)
	if got != nil {
		t.Errorf("batch with directory should be rejected, got %v", got)
	}
}

func TestParse_EmptyReturnsNil(t *testing.T) {
	if got := Parse(""); got != nil {
		t.Errorf("empty payload should return nil, got %v", got)
	}
	if got := Parse("  \t\n"); got != nil {
		t.Errorf("whitespace-only payload should return nil, got %v", got)
	}
}

func TestParse_UnbalancedQuotesReturnsNil(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "foo.txt")
	got := Parse("'" + p) // missing closing quote
	if got != nil {
		t.Errorf("unbalanced quote should return nil, got %v", got)
	}
}

func TestParse_FileURIBadEncoding(t *testing.T) {
	got := Parse("file:///bad%ZZ-encoding")
	if got != nil {
		t.Errorf("bad URL encoding should return nil, got %v", got)
	}
}

func TestParse_SymlinkToRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	target := fixture(t, dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	got := Parse(link)
	if len(got) != 1 || got[0] != link {
		t.Errorf("symlink to file: want [%q], got %v", link, got)
	}
}
