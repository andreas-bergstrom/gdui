package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreas-bergstrom/gdui/internal/filter"
	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/tree"
)

func mkChanged(path string) git.ChangedFile {
	return git.ChangedFile{Path: path, Kind: git.Modified}
}

func compileMust(t *testing.T, pattern string) *filter.Matcher {
	t.Helper()
	m, err := filter.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return m
}

// rowPaths returns the Path of every treeRow node in row-list order. Useful
// for asserting filter visibility succinctly.
func rowPaths(rows []displayRow) []string {
	out := []string{}
	for _, r := range rows {
		if t, ok := r.(treeRow); ok && t.node != nil {
			out = append(out, t.node.Path)
		}
	}
	return out
}

func TestFlattenWithFilter_FileMatch(t *testing.T) {
	root := tree.Build([]git.ChangedFile{
		mkChanged("internal/git/status.go"),
		mkChanged("internal/ui/model.go"),
		mkChanged("README.md"),
	})
	m := compileMust(t, "status")
	got := rowPaths(toRows(flattenWithFilter(root, m)))
	// Should keep status.go and its ancestor dirs; drop everything else.
	want := map[string]bool{
		"internal":         true,
		"internal/git":     true,
		"internal/git/status.go": true,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d (%v)", len(want), len(got), got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path in filtered output: %q", p)
		}
	}
}

func TestFlattenWithFilter_AutoExpandsCollapsedDirs(t *testing.T) {
	root := tree.Build([]git.ChangedFile{
		mkChanged("a/deep/nested/match.go"),
		mkChanged("a/deep/nested/other.go"),
	})
	// Collapse all directories — filter must override Expanded to reveal matches.
	collapseAll(root)
	m := compileMust(t, "match")
	got := rowPaths(toRows(flattenWithFilter(root, m)))
	wantContains := []string{"a/deep/nested", "a/deep/nested/match.go"}
	for _, p := range wantContains {
		found := false
		for _, g := range got {
			if g == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("filter should auto-reveal %q in collapsed tree; got %v", p, got)
		}
	}
}

func TestFlattenWithFilter_NoMatchesYieldsEmpty(t *testing.T) {
	root := tree.Build([]git.ChangedFile{
		mkChanged("foo.go"),
		mkChanged("bar.md"),
	})
	m := compileMust(t, "noplease")
	got := flattenWithFilter(root, m)
	if len(got) != 0 {
		t.Fatalf("no-match filter should produce empty output, got %d rows", len(got))
	}
}

func TestFlattenSectionsFiltered_KeepsHeaders(t *testing.T) {
	a := mkSection("/repo/a", "main", mkChanged("foo.go"))
	b := mkSection("/repo/b", "feat", mkChanged("bar.md"))
	m := compileMust(t, "foo")
	rows := flattenSectionsFiltered([]*WorktreeSection{a, b}, true, m)
	// Both section headers must remain even though section b has no matches.
	headers := 0
	for _, r := range rows {
		if _, ok := r.(headerRow); ok {
			headers++
		}
	}
	if headers != 2 {
		t.Errorf("expected 2 section headers preserved, got %d", headers)
	}
	paths := rowPaths(rows)
	for _, p := range paths {
		if p == "bar.md" {
			t.Errorf("section b's bar.md should be filtered out")
		}
	}
}

func TestFlattenSectionsFiltered_NilMatcherDelegates(t *testing.T) {
	s := mkSection("/repo", "main",
		mkChanged("foo.go"),
		mkChanged("bar.md"),
	)
	gotFiltered := flattenSectionsFiltered([]*WorktreeSection{s}, false, nil)
	gotPlain := flattenSections([]*WorktreeSection{s}, false)
	if len(gotFiltered) != len(gotPlain) {
		t.Fatalf("nil matcher should produce identical output to flattenSections (%d vs %d)",
			len(gotFiltered), len(gotPlain))
	}
}

func TestFlattenWithFilter_DoesNotMutateExpanded(t *testing.T) {
	root := tree.Build([]git.ChangedFile{
		mkChanged("a/b/c.go"),
	})
	collapseAll(root)
	// Snapshot Expanded states before filtering.
	before := snapshotExpanded(root)
	m := compileMust(t, "c.go")
	_ = flattenWithFilter(root, m)
	after := snapshotExpanded(root)
	if !equalBoolMap(before, after) {
		t.Errorf("flattenWithFilter must not mutate Expanded; before=%v after=%v", before, after)
	}
}

// --- model-driven integration tests ---

// modelWithSection returns a Model in tree mode with one section containing
// the given changed files, ready to receive key events.
func modelWithSection(t *testing.T, paths ...string) Model {
	t.Helper()
	files := make([]git.ChangedFile, len(paths))
	for i, p := range paths {
		files[i] = mkChanged(p)
	}
	s := mkSection("/repo", "main", files...)
	m := New("/repo")
	m.sections = []*WorktreeSection{s}
	m.activeWT = 0
	m.ready = true
	m.width = 80
	m.height = 24
	m.refreshView()
	return m
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// asModel coerces handleKey's tea.Model return to a *Model regardless of
// whether the branch returned `m` (pointer) or `*m` (value). The codebase
// is inconsistent on this — both forms are used across switch arms.
func asModel(t *testing.T, mi tea.Model) *Model {
	t.Helper()
	switch v := mi.(type) {
	case *Model:
		return v
	case Model:
		return &v
	}
	t.Fatalf("unexpected tea.Model type %T", mi)
	return nil
}

func TestModel_FilterKeyEntersEditing(t *testing.T) {
	m := modelWithSection(t, "foo.go", "bar.go")
	mi, _ := m.handleKey(runeKey("f"))
	got := asModel(t, mi)
	if !got.filter.editing {
		t.Errorf("expected filter.editing=true after pressing f")
	}
}

func TestModel_FilterTypingShrinksRows(t *testing.T) {
	m := modelWithSection(t, "foo.go", "bar.go", "baz.md")
	mi, _ := m.handleKey(runeKey("f"))
	mm := asModel(t, mi)
	mi, _ = mm.handleKey(runeKey("foo"))
	mm = asModel(t, mi)
	if !mm.filter.active() {
		t.Fatal("expected active filter after typing")
	}
	paths := rowPaths(mm.rows)
	for _, p := range paths {
		if p == "bar.go" || p == "baz.md" {
			t.Errorf("filter %q should hide %q", "foo", p)
		}
	}
	foundFoo := false
	for _, p := range paths {
		if p == "foo.go" {
			foundFoo = true
		}
	}
	if !foundFoo {
		t.Error("filter should keep foo.go")
	}
}

func TestModel_FilterEnterCommitsAndExitsEditing(t *testing.T) {
	m := modelWithSection(t, "foo.go", "bar.go")
	mi, _ := m.handleKey(runeKey("f"))
	mi, _ = asModel(t, mi).handleKey(runeKey("foo"))
	mi, _ = asModel(t, mi).handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := asModel(t, mi)
	if mm.filter.editing {
		t.Errorf("Enter should exit editing")
	}
	if !mm.filter.active() {
		t.Errorf("Enter should preserve the filter when query is non-empty")
	}
}

func TestModel_FilterEscClearsEverything(t *testing.T) {
	m := modelWithSection(t, "foo.go", "bar.go")
	mi, _ := m.handleKey(runeKey("f"))
	mi, _ = asModel(t, mi).handleKey(runeKey("foo"))
	mi, _ = asModel(t, mi).handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	mm := asModel(t, mi)
	if mm.filter.editing || mm.filter.active() || mm.filter.query != "" {
		t.Errorf("Esc should fully reset filter; got %+v", mm.filter)
	}
	if len(rowPaths(mm.rows)) != 2 {
		t.Errorf("expected both files visible again after clear, got %v", rowPaths(mm.rows))
	}
}

func TestModel_FilterEnterEmptyClears(t *testing.T) {
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(runeKey("f"))
	mi, _ = asModel(t, mi).handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := asModel(t, mi)
	if mm.filter.editing || mm.filter.active() {
		t.Errorf("Enter on empty query should clear filter; got %+v", mm.filter)
	}
}

func TestModel_FilterRePressPreservesQuery(t *testing.T) {
	m := modelWithSection(t, "foo.go", "bar.go")
	mi, _ := m.handleKey(runeKey("f"))
	mi, _ = asModel(t, mi).handleKey(runeKey("foo"))
	mi, _ = asModel(t, mi).handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mi, _ = asModel(t, mi).handleKey(runeKey("f"))
	mm := asModel(t, mi)
	if !mm.filter.editing {
		t.Errorf("re-pressing f should re-enter editing")
	}
	if mm.filter.query != "foo" {
		t.Errorf("expected query preserved as %q, got %q", "foo", mm.filter.query)
	}
}

func TestModel_FilterErrorDoesNotCrash(t *testing.T) {
	m := modelWithSection(t, "foo.go")
	mi, _ := m.handleKey(runeKey("f"))
	mi, _ = asModel(t, mi).handleKey(runeKey("re:["))
	mm := asModel(t, mi)
	if mm.filter.err == nil {
		t.Errorf("expected compilation error for re:[")
	}
	if mm.filter.matcher != nil {
		t.Errorf("expected nil matcher when compile fails")
	}
	_ = mm.View()
}

// --- helpers ---

// toRows wraps tree.Node slice into a displayRow slice for rowPaths().
func toRows(nodes []*tree.Node) []displayRow {
	out := make([]displayRow, len(nodes))
	for i, n := range nodes {
		out[i] = treeRow{sectionIdx: -1, node: n}
	}
	return out
}

func collapseAll(n *tree.Node) {
	if n == nil {
		return
	}
	if n.IsDir {
		n.Expanded = false
	}
	for _, c := range n.Children {
		collapseAll(c)
	}
}

func snapshotExpanded(n *tree.Node) map[string]bool {
	out := map[string]bool{}
	var walk func(x *tree.Node)
	walk = func(x *tree.Node) {
		if x == nil {
			return
		}
		out[x.Path] = x.Expanded
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return out
}

func equalBoolMap(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
