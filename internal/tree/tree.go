package tree

import (
	"sort"
	"strings"

	"github.com/andreasbergstrom/gd/internal/git"
)

type Node struct {
	Name        string
	Path        string
	IsDir       bool
	Parent      *Node
	Children    []*Node
	File        *git.ChangedFile // nil for unchanged files in "all" mode
	Adds        int
	Dels        int
	Expanded    bool
	Hunks       []git.Hunk
	Loading     bool
	LoadErr     error
	Interesting bool // dir contains a changed descendant, or file is itself changed
}

// Build constructs a sparse tree containing only changed paths.
// Folders with a single child folder are path-collapsed into one row.
func Build(files []git.ChangedFile) *Node {
	root := &Node{IsDir: true, Expanded: true}
	for i := range files {
		f := files[i]
		insert(root, &f)
	}
	sortRec(root)
	collapseChains(root)
	aggregate(root)
	markInteresting(root)
	return root
}

// BuildAll constructs the full repo tree, with `changed` overlaying File
// pointers and counts on the corresponding leaves. Unchanged files appear
// as plain leaves with File==nil. Single-child collapsing is disabled in
// this mode (it would hide most of the repo structure).
func BuildAll(changed []git.ChangedFile, allPaths []string) *Node {
	root := &Node{IsDir: true, Expanded: true}
	for i := range changed {
		f := changed[i]
		insert(root, &f)
	}
	for _, p := range allPaths {
		if p == "" {
			continue
		}
		insertPlain(root, p)
	}
	sortRec(root)
	aggregate(root)
	markInteresting(root)
	defaultExpandInteresting(root)
	return root
}

func insert(root *Node, f *git.ChangedFile) {
	parts := strings.Split(f.Path, "/")
	cur := root
	for i, seg := range parts {
		isLeaf := i == len(parts)-1
		var child *Node
		for _, c := range cur.Children {
			if c.Name == seg && c.IsDir == !isLeaf {
				child = c
				break
			}
		}
		if child == nil {
			path := seg
			if cur.Path != "" {
				path = cur.Path + "/" + seg
			}
			child = &Node{Name: seg, Path: path, IsDir: !isLeaf, Parent: cur, Expanded: !isLeaf}
			if isLeaf {
				child.File = f
			}
			cur.Children = append(cur.Children, child)
		}
		cur = child
	}
}

// insertPlain inserts a path as an unchanged file; if the leaf already exists
// (because it was inserted as a changed file), it's left untouched.
func insertPlain(root *Node, path string) {
	parts := strings.Split(path, "/")
	cur := root
	for i, seg := range parts {
		isLeaf := i == len(parts)-1
		var child *Node
		for _, c := range cur.Children {
			if c.Name == seg && c.IsDir == !isLeaf {
				child = c
				break
			}
		}
		if child == nil {
			p := seg
			if cur.Path != "" {
				p = cur.Path + "/" + seg
			}
			child = &Node{Name: seg, Path: p, IsDir: !isLeaf, Parent: cur, Expanded: false}
			cur.Children = append(cur.Children, child)
		}
		cur = child
	}
}

func sortRec(n *Node) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	for _, c := range n.Children {
		sortRec(c)
	}
}

// collapseChains walks the tree and merges directory chains with a single
// directory child into a single row, e.g. "src/foo/bar".
func collapseChains(n *Node) {
	for _, c := range n.Children {
		collapseChains(c)
	}
	if !n.IsDir || n.Parent == nil {
		return
	}
	for len(n.Children) == 1 && n.Children[0].IsDir {
		only := n.Children[0]
		n.Name = n.Name + "/" + only.Name
		n.Path = only.Path
		n.Children = only.Children
		for _, c := range n.Children {
			c.Parent = n
		}
	}
}

func aggregate(n *Node) (adds, dels int) {
	if !n.IsDir && n.File != nil {
		n.Adds, n.Dels = n.File.Adds, n.File.Dels
		return n.Adds, n.Dels
	}
	for _, c := range n.Children {
		a, d := aggregate(c)
		adds += a
		dels += d
	}
	n.Adds, n.Dels = adds, dels
	return
}

func markInteresting(n *Node) bool {
	if !n.IsDir {
		n.Interesting = n.File != nil
		return n.Interesting
	}
	any := false
	for _, c := range n.Children {
		if markInteresting(c) {
			any = true
		}
	}
	n.Interesting = any
	return any
}

// defaultExpandInteresting opens directories that contain changed descendants;
// leaves uninteresting directories collapsed.
func defaultExpandInteresting(n *Node) {
	if !n.IsDir {
		return
	}
	n.Expanded = n.Interesting
	for _, c := range n.Children {
		defaultExpandInteresting(c)
	}
}

// Flatten returns the visible nodes in display order, given current expand state.
// Root itself is not emitted; root's children are.
func Flatten(root *Node) []*Node {
	var out []*Node
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		out = append(out, n)
		if n.IsDir && n.Expanded {
			for _, c := range n.Children {
				walk(c, depth+1)
			}
		}
	}
	for _, c := range root.Children {
		walk(c, 0)
	}
	return out
}

// FindByPath returns the node with the given repo-relative path, or nil.
// Used to re-resolve a node after the tree has been rebuilt by a refresh,
// since holding pointers across rebuilds is unsafe.
func FindByPath(root *Node, path string) *Node {
	if root == nil || path == "" {
		return nil
	}
	var found *Node
	var walk func(n *Node) bool
	walk = func(n *Node) bool {
		if n.Path == path {
			found = n
			return true
		}
		for _, c := range n.Children {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// Depth returns the display indentation depth of a node (root children = 0).
func Depth(n *Node) int {
	d := -1
	for cur := n; cur != nil && cur.Parent != nil; cur = cur.Parent {
		d++
	}
	if d < 0 {
		d = 0
	}
	return d
}
