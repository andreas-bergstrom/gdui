package git

import "testing"

func TestParseUnified_NoTrailingNewline(t *testing.T) {
	in := "diff --git a/x b/x\n" +
		"index 1..2 100644\n" +
		"--- a/x\n+++ b/x\n" +
		"@@ -1,2 +1,2 @@\n" +
		" foo\n" +
		"-bar\n" +
		"+baz\n" +
		"\\ No newline at end of file\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hs))
	}
	if len(hs[0].Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(hs[0].Lines))
	}
	if !hs[0].Lines[2].NoNewlineHere {
		t.Fatalf("expected last line to be marked NoNewlineHere")
	}
	if hs[0].Lines[2].Text != "baz" {
		t.Fatalf("got %q", hs[0].Lines[2].Text)
	}
}

func TestParseUnified_Binary(t *testing.T) {
	in := "diff --git a/x b/x\nBinary files a/x and b/x differ\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 0 {
		t.Fatalf("expected 0 hunks for binary, got %d", len(hs))
	}
}

func TestParseUnified_MultiHunk(t *testing.T) {
	in := "diff --git a/x b/x\n" +
		"--- a/x\n+++ b/x\n" +
		"@@ -1,1 +1,2 @@\n a\n+b\n" +
		"@@ -10,1 +11,2 @@\n c\n+d\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hs))
	}
	if hs[0].Lines[1].Kind != '+' || hs[0].Lines[1].Text != "b" {
		t.Fatalf("hunk 1 mismatch: %+v", hs[0].Lines)
	}
	if hs[1].Lines[1].Text != "d" {
		t.Fatalf("hunk 2 mismatch: %+v", hs[1].Lines)
	}
}

func TestParseUnified_Rename(t *testing.T) {
	in := "diff --git a/old b/new\n" +
		"similarity index 90%\n" +
		"rename from old\n" +
		"rename to new\n" +
		"--- a/old\n+++ b/new\n" +
		"@@ -1,1 +1,1 @@\n-foo\n+bar\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 1 || len(hs[0].Lines) != 2 {
		t.Fatalf("rename parse mismatch: %+v", hs)
	}
}

func TestParseUnified_DeletedFile(t *testing.T) {
	in := "diff --git a/x b/x\n" +
		"deleted file mode 100644\n" +
		"index 1..0\n" +
		"--- a/x\n+++ /dev/null\n" +
		"@@ -1,2 +0,0 @@\n-a\n-b\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 1 || len(hs[0].Lines) != 2 ||
		hs[0].Lines[0].Kind != '-' || hs[0].Lines[1].Kind != '-' {
		t.Fatalf("deleted parse mismatch: %+v", hs)
	}
}

func TestParseUnified_CRLFPreserved(t *testing.T) {
	in := "diff --git a/x b/x\n--- a/x\n+++ b/x\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-foo\r\n" +
		"+bar\r\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 1 || len(hs[0].Lines) != 2 {
		t.Fatalf("crlf hunk shape mismatch")
	}
	if hs[0].Lines[0].Text != "foo\r" || hs[0].Lines[1].Text != "bar\r" {
		t.Fatalf("crlf preservation failed: %q / %q", hs[0].Lines[0].Text, hs[0].Lines[1].Text)
	}
}

func TestParseUnified_StrayContentBeforeHunk(t *testing.T) {
	in := "diff --git a/x b/x\nrandom junk\n@@ -1,1 +1,1 @@\n a\n"
	hs := ParseUnified([]byte(in))
	if len(hs) != 1 || len(hs[0].Lines) != 1 || hs[0].Lines[0].Kind != ' ' {
		t.Fatalf("stray-before-hunk parse mismatch: %+v", hs)
	}
}
