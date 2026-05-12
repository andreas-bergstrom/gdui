package ui

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/andreas-bergstrom/gdui/internal/git"
)

func TestNestedChildPathsMap_BasicAttachment(t *testing.T) {
	// Parent at /repo, two nested repos directly under it.
	parent := "/repo"
	a := "/repo/nested-a"
	b := "/repo/sub/nested-b"
	all := []git.Worktree{
		{Root: parent},
		{Root: a},
		{Root: b},
	}
	nested := map[string]bool{a: true, b: true}

	got := nestedChildPathsMap(all, nested)

	// Both should attach to /repo (the only non-nested ancestor in the set).
	// Slash normalization: git status emits forward slashes, so we expect
	// "nested-a" and "sub/nested-b" regardless of OS.
	want := map[string][]string{
		parent: {"nested-a", "sub/nested-b"},
	}
	if !mapsEqualSorted(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNestedChildPathsMap_NestedInsideNestedAttachesToClosest(t *testing.T) {
	// /repo / nested-a / inner — `inner` should attach to nested-a, not /repo.
	parent := "/repo"
	a := "/repo/nested-a"
	inner := "/repo/nested-a/inner"
	all := []git.Worktree{
		{Root: parent},
		{Root: a},
		{Root: inner},
	}
	nested := map[string]bool{a: true, inner: true}

	got := nestedChildPathsMap(all, nested)
	want := map[string][]string{
		parent: {"nested-a"},
		a:      {"inner"},
	}
	if !mapsEqualSorted(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNestedChildPathsMap_NoNestedReposReturnsEmpty(t *testing.T) {
	all := []git.Worktree{{Root: "/repo"}, {Root: "/repo/wt-feat"}}
	got := nestedChildPathsMap(all, map[string]bool{})
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestNestedChildPathsMap_PrefixOnSeparatorBoundary(t *testing.T) {
	// "/repo/sub-extra" must not match parent "/repo/sub" — string-prefix
	// would say yes, but separator-aware prefix says no. Regression guard.
	parent := "/repo/sub"
	other := "/repo/sub-extra"
	nestedChild := "/repo/sub-extra/inner"
	all := []git.Worktree{
		{Root: parent},
		{Root: other},
		{Root: nestedChild},
	}
	nested := map[string]bool{nestedChild: true}

	got := nestedChildPathsMap(all, nested)
	// nestedChild lives under /repo/sub-extra (which itself is in `all`),
	// NOT under /repo/sub. So the attachment must go to /repo/sub-extra.
	want := map[string][]string{other: {"inner"}}
	if !mapsEqualSorted(got, want) {
		t.Errorf("prefix-boundary attachment wrong: got %v, want %v", got, want)
	}
}

func TestNestedSectionLabel_LongestParentWins(t *testing.T) {
	// Two parents could host the nested repo; pick the deepest (closest).
	parents := []string{"/repo", "/repo/wt-feat"}
	nested := "/repo/wt-feat/nested-a"
	got := nestedSectionLabel(nested, parents)
	if got != "nested-a" {
		t.Errorf("got %q, want %q (relative to closest parent)", got, "nested-a")
	}
}

func TestNestedSectionLabel_FallsBackToBaseName(t *testing.T) {
	// Nested repo not under any known parent — gdui launched from inside
	// a nested repo, parent unknown. Use base name as a sensible label.
	got := nestedSectionLabel("/elsewhere/foo", []string{"/repo"})
	if got != "foo" {
		t.Errorf("got %q, want %q (fallback to base)", got, "foo")
	}
}

func TestNestedSectionLabel_SeparatorBoundary(t *testing.T) {
	// "/repo-other/x" must not be considered a child of "/repo".
	got := nestedSectionLabel("/repo-other/x", []string{"/repo"})
	if got != "x" {
		t.Errorf("got %q, want %q (must use base name; /repo is not an ancestor)", got, "x")
	}
}

func TestPathExcluded(t *testing.T) {
	excl := []string{"nested-a", "sub/nested-b"}
	cases := []struct {
		path string
		want bool
	}{
		{"nested-a", true},          // exact match
		{"nested-a/file.txt", true}, // descendant
		{"nested-a.txt", false},     // sibling that shares prefix bytes
		{"sub/nested-b", true},
		{"sub/nested-b/x", true},
		{"sub/nested-bb", false},
		{"other", false},
	}
	for _, c := range cases {
		if got := pathExcluded(c.path, excl); got != c.want {
			t.Errorf("pathExcluded(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestFilterChangedFiles_Empty(t *testing.T) {
	in := []git.ChangedFile{{Path: "a"}, {Path: "b"}}
	out := filterChangedFiles(in, nil)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("empty exclude must return input unchanged: got %v", out)
	}
}

// mapsEqualSorted compares two map[string][]string after sorting each slice
// in place (the order of children under a parent isn't a contract we want
// to depend on in tests).
func mapsEqualSorted(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		ac := append([]string(nil), av...)
		bc := append([]string(nil), bv...)
		sort.Strings(ac)
		sort.Strings(bc)
		if !reflect.DeepEqual(ac, bc) {
			return false
		}
	}
	return true
}

// Ensure filepath package isn't unused in this file on any platform.
var _ = filepath.Separator
