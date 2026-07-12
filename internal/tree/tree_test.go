package tree

import (
	"strings"
	"testing"

	"github.com/andreas-bergstrom/gdui/internal/git"
)

func sample() []git.ChangedFile {
	return []git.ChangedFile{
		{Path: "src/foo/bar/main.go", Kind: git.Modified, Adds: 3, Dels: 1},
		{Path: "src/foo/bar/util.go", Kind: git.Added, Adds: 10, Dels: 0},
		{Path: "README.md", Kind: git.Modified, Adds: 1, Dels: 1},
		{Path: "docs/intro.md", Kind: git.Untracked, Adds: 5, Dels: 0},
	}
}

func TestBuild_AggregatesAddsAndDels(t *testing.T) {
	root := Build(sample())
	if root.Adds != 19 || root.Dels != 2 {
		t.Fatalf("root counts: got +%d -%d, want +19 -2", root.Adds, root.Dels)
	}
}

func TestBuild_AggregatesChangedFiles(t *testing.T) {
	root := Build(sample())
	if root.ChangedFiles != 4 {
		t.Fatalf("root ChangedFiles = %d, want 4", root.ChangedFiles)
	}
	src := FindByPath(root, "src/foo/bar")
	if src == nil || src.ChangedFiles != 2 {
		t.Fatalf("src/foo/bar ChangedFiles = %v, want 2", src)
	}
	leaf := FindByPath(root, "README.md")
	if leaf == nil || leaf.ChangedFiles != 1 {
		t.Fatalf("leaf ChangedFiles = %v, want 1", leaf)
	}
}

func TestBuildAll_ChangedFilesExcludesUnchanged(t *testing.T) {
	changed := []git.ChangedFile{
		{Path: "src/main.go", Kind: git.Modified, Adds: 2, Dels: 1},
	}
	all := []string{"src/main.go", "src/util.go", "README.md"}
	root := BuildAll(changed, all)
	src := FindByPath(root, "src")
	if src == nil || src.ChangedFiles != 1 {
		t.Fatalf("src ChangedFiles = %v, want 1 (util.go unchanged)", src)
	}
	if root.ChangedFiles != 1 {
		t.Fatalf("root ChangedFiles = %d, want 1", root.ChangedFiles)
	}
}

func TestBuild_DirsBeforeFiles(t *testing.T) {
	root := Build(sample())
	// At root level, we have docs/, src/, README.md after collapsing — dirs first.
	prevIsDir := true
	for _, c := range root.Children {
		if !c.IsDir && prevIsDir {
			prevIsDir = false
			continue
		}
		if c.IsDir && !prevIsDir {
			t.Fatalf("file appeared before dir: %s", c.Name)
		}
	}
}

func TestBuild_CollapsesSingleChildChain(t *testing.T) {
	files := []git.ChangedFile{
		{Path: "a/b/c/leaf.go", Kind: git.Modified},
	}
	root := Build(files)
	if len(root.Children) != 1 {
		t.Fatalf("want 1 child at root, got %d", len(root.Children))
	}
	c := root.Children[0]
	if c.Name != "a/b/c" {
		t.Fatalf("want collapsed name 'a/b/c', got %q", c.Name)
	}
	if len(c.Children) != 1 || c.Children[0].Name != "leaf.go" {
		t.Fatalf("collapse lost leaf: %+v", c.Children)
	}
}

func TestBuild_NoCollapseWhenSiblings(t *testing.T) {
	files := []git.ChangedFile{
		{Path: "a/b/x.go", Kind: git.Modified},
		{Path: "a/c/y.go", Kind: git.Modified},
	}
	root := Build(files)
	// Root has one child "a" with two dir children "b" and "c" — should NOT collapse.
	if len(root.Children) != 1 || root.Children[0].Name != "a" {
		t.Fatalf("unexpected top-level: %+v", root.Children)
	}
	a := root.Children[0]
	if len(a.Children) != 2 {
		t.Fatalf("expected 2 children of a/, got %d", len(a.Children))
	}
}

func TestFlatten_ExpandedOnlyDescent(t *testing.T) {
	root := Build(sample())
	rows := Flatten(root)
	if len(rows) == 0 {
		t.Fatal("flatten produced no rows")
	}

	// Collapse all dirs except root and re-flatten — should be just top-level rows.
	var collapseAll func(n *Node)
	collapseAll = func(n *Node) {
		if n.IsDir && n.Parent != nil {
			n.Expanded = false
		}
		for _, c := range n.Children {
			collapseAll(c)
		}
	}
	collapseAll(root)
	rows2 := Flatten(root)
	if len(rows2) != len(root.Children) {
		t.Fatalf("collapsed flatten = %d rows, want %d", len(rows2), len(root.Children))
	}
}

func TestFindByPath_Hit(t *testing.T) {
	root := Build(sample())
	got := FindByPath(root, "README.md")
	if got == nil || got.Name != "README.md" {
		t.Fatalf("FindByPath('README.md') = %v", got)
	}
}

func TestFindByPath_Miss(t *testing.T) {
	root := Build(sample())
	if got := FindByPath(root, "nope/missing.go"); got != nil {
		t.Fatalf("expected nil for missing path, got %+v", got)
	}
}

func TestFindByPath_NilSafe(t *testing.T) {
	if FindByPath(nil, "anything") != nil {
		t.Fatal("FindByPath(nil) should return nil")
	}
	if FindByPath(&Node{}, "") != nil {
		t.Fatal("FindByPath with empty path should return nil")
	}
}

func TestDepth_RootChildrenAreZero(t *testing.T) {
	root := Build(sample())
	for _, c := range root.Children {
		if d := Depth(c); d != 0 {
			t.Errorf("Depth(%s) = %d, want 0", c.Name, d)
		}
	}
}

func TestDepth_NestedIncreases(t *testing.T) {
	files := []git.ChangedFile{{Path: "a/b/c/x.go", Kind: git.Modified}}
	root := Build(files)
	leaf := FindByPath(root, "a/b/c/x.go")
	if leaf == nil {
		t.Fatal("leaf not found")
	}
	// After collapse, the "a/b/c" dir is depth 0, leaf is depth 1.
	if d := Depth(leaf); d != 1 {
		t.Errorf("leaf Depth = %d, want 1 (collapsed chain)", d)
	}
}

func TestBuildAll_OverlaysChangedOnPlain(t *testing.T) {
	changed := []git.ChangedFile{
		{Path: "src/main.go", Kind: git.Modified, Adds: 2, Dels: 1},
	}
	all := []string{"src/main.go", "src/util.go", "README.md"}
	root := BuildAll(changed, all)
	main := FindByPath(root, "src/main.go")
	util := FindByPath(root, "src/util.go")
	if main == nil || main.File == nil {
		t.Fatalf("changed leaf missing File ptr")
	}
	if util == nil || util.File != nil {
		t.Fatalf("unchanged leaf should have File==nil, got %+v", util.File)
	}
}

func TestBuildAll_ExpandsInterestingDirs(t *testing.T) {
	changed := []git.ChangedFile{{Path: "src/foo.go", Kind: git.Modified}}
	all := []string{"src/foo.go", "docs/x.md"}
	root := BuildAll(changed, all)
	for _, c := range root.Children {
		if c.Name == "src" && !c.Expanded {
			t.Error("interesting dir 'src' should be expanded")
		}
		if c.Name == "docs" && c.Expanded {
			t.Error("uninteresting dir 'docs' should not be expanded")
		}
	}
}

func TestMarkInteresting_PropagatesUp(t *testing.T) {
	changed := []git.ChangedFile{{Path: "deep/nest/x.go", Kind: git.Modified}}
	root := BuildAll(changed, []string{"deep/nest/x.go", "other/y.go"})
	deep := FindByPath(root, "deep")
	other := FindByPath(root, "other")
	if deep == nil || !deep.Interesting {
		t.Errorf("deep/ should be Interesting")
	}
	if other == nil || other.Interesting {
		t.Errorf("other/ should not be Interesting")
	}
}

func TestBuild_SortsCaseSensitive(t *testing.T) {
	files := []git.ChangedFile{
		{Path: "Z.txt", Kind: git.Modified},
		{Path: "a.txt", Kind: git.Modified},
	}
	root := Build(files)
	var names []string
	for _, c := range root.Children {
		names = append(names, c.Name)
	}
	got := strings.Join(names, ",")
	if got != "Z.txt,a.txt" {
		t.Errorf("sort order: %s", got)
	}
}
