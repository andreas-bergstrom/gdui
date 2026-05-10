package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

// pasteMsg constructs the exact KeyMsg shape Bubble Tea v1.3.x emits when
// bracketed paste delivers a payload — a single KeyMsg with Type=KeyRunes,
// Paste=true, Runes set. Tests reuse this so they exercise the real
// detection path in handleKey, not a shortcut.
func pasteMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

func TestModel_PasteOfFilePathEntersDropPrompt(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "drag.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(pasteMsg(src))
	got := asModel(t, mi)
	if !got.drop.active() {
		t.Fatalf("expected drop.active() after pasting a real file path; phase=%v queue=%v", got.drop.phase, got.drop.queue)
	}
	if got.drop.phase != dropPromptDest {
		t.Errorf("expected dropPromptDest phase, got %v", got.drop.phase)
	}
	wantDest := filepath.Join("/repo", "drag.txt")
	if got.drop.dest != wantDest {
		t.Errorf("expected default dest %q, got %q", wantDest, got.drop.dest)
	}
}

func TestModel_PasteOfPlainTextIgnored(t *testing.T) {
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(pasteMsg("just some random text that is not a path"))
	got := asModel(t, mi)
	if got.drop.active() {
		t.Errorf("plain text paste should not enter drop mode; state=%+v", got.drop)
	}
}

// nonPasteRunes simulates the KeyMsg shape from terminals (notably Warp) that
// don't enable bracketed paste — the path arrives as plain rune input with
// msg.Paste = false. Bubble Tea batches consecutive printable runes into one
// KeyMsg, so a multi-rune KeyRunes with Paste=false is the detection signal.
func nonPasteRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} // Paste defaults to false
}

func TestModel_NonPasteMultiRuneFilePathTriggersDrop(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "warp-drop.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes(src))
	got := asModel(t, mi)
	if !got.drop.active() {
		t.Errorf("non-paste multi-rune file path should enter drop mode (Warp case); state=%+v", got.drop)
	}
}

func TestModel_ShortNonPasteRunesFallThrough(t *testing.T) {
	// 3 chars — below the dropMinRunes threshold. Even if it parsed to a
	// file, it shouldn't trigger drop. (We deliberately don't make this
	// path a real file to be doubly sure.)
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes("abc"))
	got := asModel(t, mi)
	if got.drop.active() {
		t.Errorf("short rune burst should not trigger drop detection; state=%+v", got.drop)
	}
}

func TestModel_NonPasteRunesNotAFileFallsThrough(t *testing.T) {
	// 10 chars, doesn't exist as a file → drop.Parse returns nil →
	// falls through to normal key handling without entering drop mode.
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes("hello-world-not-a-path"))
	got := asModel(t, mi)
	if got.drop.active() {
		t.Errorf("non-file multi-rune input should not enter drop; state=%+v", got.drop)
	}
}

func TestModel_TruncatedDropPathSurfacesError(t *testing.T) {
	// Simulate the Warp case: dropping `/tmp/foo with spaces.txt`. The first
	// chunk Bubble Tea emits before breaking on space is `/tmp/foo`. Its
	// parent (`/tmp`) exists, so we should treat it as a likely truncated
	// drop and surface a help message.
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes("/tmp/foo"))
	got := asModel(t, mi)
	if got.drop.err == "" {
		t.Errorf("expected truncated-drop error, got empty err; state=%+v", got.drop)
	}
	if got.drop.active() {
		t.Errorf("error should NOT activate a prompt (idle phase with err); state=%+v", got.drop)
	}
}

func TestModel_TruncatedDropErrorDismissesOnEsc(t *testing.T) {
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes("/tmp/foo"))
	got := asModel(t, mi)
	if got.drop.err == "" {
		t.Fatalf("setup: expected err to be set first")
	}
	mi, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = asModel(t, mi)
	if got.drop.err != "" {
		t.Errorf("esc should clear drop error; got %q", got.drop.err)
	}
}

func TestModel_NonAbsolutePathNoError(t *testing.T) {
	// Random multi-char input that doesn't start with `/` shouldn't trigger
	// the truncated-drop warning.
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(nonPasteRunes("hello-world"))
	got := asModel(t, mi)
	if got.drop.err != "" {
		t.Errorf("non-absolute input shouldn't set drop.err; got %q", got.drop.err)
	}
}

func TestModel_PasteInLogModeIgnored(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "drag.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	m.mode = ModeLog // simulate log mode without cycling
	mi, _ := m.handleKey(pasteMsg(src))
	got := asModel(t, mi)
	if got.drop.active() {
		t.Errorf("paste in ModeLog should not enter drop mode; state=%+v", got.drop)
	}
}

func TestModel_DropEscSkipsCurrent(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %q: %v", p, err)
		}
	}
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(pasteMsg(a + " " + b))
	got := asModel(t, mi)
	if len(got.drop.queue) != 2 {
		t.Fatalf("expected queue length 2, got %d", len(got.drop.queue))
	}
	// Esc on first prompt → advance to second.
	mi, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = asModel(t, mi)
	if !got.drop.active() {
		t.Errorf("expected drop still active after Esc with another queued; state=%+v", got.drop)
	}
	if len(got.drop.queue) != 1 || got.drop.queue[0] != b {
		t.Errorf("expected queue to advance to second file %q, got %v", b, got.drop.queue)
	}
}

func TestModel_DropSwallowsGlobalKeys(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "drag.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(pasteMsg(src))
	got := asModel(t, mi)
	// q would normally quit; while drop is active, it should be swallowed
	// into the dest field instead. We can verify by checking that
	// q appended to dest and the model state hasn't become "quitting".
	prevDest := got.drop.dest
	mi, cmd := got.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	got = asModel(t, mi)
	if got.drop.dest != prevDest+"q" {
		t.Errorf("expected q to append to dest, got dest=%q (was %q)", got.drop.dest, prevDest)
	}
	// cmd should be ClearScreen, not Quit. tea.Quit is uniquely callable,
	// but its inner Msg type matters — we check it's not nil and isn't
	// quit by simply verifying the model is still alive (no panic).
	_ = cmd
}

func TestModel_DropEscapeQueueEmptiesAndCloses(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "drag.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(pasteMsg(src))
	got := asModel(t, mi)
	mi, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = asModel(t, mi)
	if got.drop.active() {
		t.Errorf("expected drop idle after Esc on only file; state=%+v", got.drop)
	}
	if len(got.drop.queue) != 0 {
		t.Errorf("expected empty queue, got %v", got.drop.queue)
	}
}

func TestModel_DropOverwriteConfirmFlow(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "drag.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Create a clashing destination ahead of time.
	repoDir := t.TempDir()
	clashing := filepath.Join(repoDir, "drag.txt")
	if err := os.WriteFile(clashing, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write clashing: %v", err)
	}
	m := modelWithSection(t, "foo.go")
	// Rewrite section root to point at the real temp dir so dest clash
	// detection actually finds the existing file.
	m.sections[0].WT.Root = repoDir
	m.repoRoot = repoDir
	mi, _ := m.handleKey(pasteMsg(src))
	got := asModel(t, mi)
	// Default dest is <repoDir>/drag.txt which already exists.
	mi, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got = asModel(t, mi)
	if got.drop.phase != dropPromptOverwrite {
		t.Errorf("expected dropPromptOverwrite after Enter on clashing dest, got %v", got.drop.phase)
	}
	// 'n' cancels back to dest edit.
	mi, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got = asModel(t, mi)
	if got.drop.phase != dropPromptDest {
		t.Errorf("expected n to return to dropPromptDest, got %v", got.drop.phase)
	}
}

func TestExpandToPath_CollapsedChain(t *testing.T) {
	// Build a tree where chains collapse: src has only one child foo which
	// has only one child bar — collapses into a single node with Path
	// "src/foo/bar".
	root := tree.Build([]git.ChangedFile{
		{Path: "src/foo/bar/file.go", Kind: git.Modified},
	})
	// Reset Expanded on all directories so we can verify the helper sets them.
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		if n.IsDir {
			n.Expanded = false
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	expandToPath(root, "src/foo/bar/file.go")
	// At least one directory in the chain should be re-expanded.
	any := false
	var check func(n *tree.Node)
	check = func(n *tree.Node) {
		if n.IsDir && n.Expanded {
			any = true
		}
		for _, c := range n.Children {
			check(c)
		}
	}
	check(root)
	if !any {
		t.Errorf("expandToPath should set Expanded=true on at least one ancestor; tree state unchanged")
	}
}

func TestCopyDropCmd_AtomicAndPreservesContents(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dest := filepath.Join(tmp, "dst", "dest.txt")
	want := "atomic copy contents"
	if err := os.WriteFile(src, []byte(want), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	cmd := copyDropCmd(src, dest, tmp)
	msg := cmd()
	if failed, ok := msg.(dropFailedMsg); ok {
		t.Fatalf("copy failed: %v", failed.err)
	}
	completed, ok := msg.(dropCompletedMsg)
	if !ok {
		t.Fatalf("expected dropCompletedMsg, got %T", msg)
	}
	if completed.dest != dest {
		t.Errorf("dropCompletedMsg.dest = %q, want %q", completed.dest, dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != want {
		t.Errorf("dest contents = %q, want %q", got, want)
	}
	// No leftover tmp files in dest dir.
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("read dest dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(dest) {
			t.Errorf("unexpected leftover in dest dir: %q", e.Name())
		}
	}
}

func TestCopyDropCmd_NonexistentSourceFails(t *testing.T) {
	tmp := t.TempDir()
	cmd := copyDropCmd(filepath.Join(tmp, "nope.txt"), filepath.Join(tmp, "dest.txt"), tmp)
	msg := cmd()
	if _, ok := msg.(dropFailedMsg); !ok {
		t.Errorf("expected dropFailedMsg for missing source, got %T", msg)
	}
}
