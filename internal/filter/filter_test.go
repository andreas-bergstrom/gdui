package filter

import "testing"

func TestCompile_EmptyReturnsNilMatcher(t *testing.T) {
	m, err := Compile("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatalf("empty pattern should compile to nil matcher, got %#v", m)
	}
}

func TestNilMatcher_MatchesEverything(t *testing.T) {
	var m *Matcher
	for _, s := range []string{"", "anything", "internal/git/status.go"} {
		if !m.Match(s) {
			t.Errorf("nil matcher should match %q", s)
		}
	}
}

func TestCompile_InvalidRegex(t *testing.T) {
	if _, err := Compile("re:[invalid"); err == nil {
		t.Fatal("expected compile error for unbalanced bracket regex")
	}
}

func TestCompile_UnbalancedGlobBracketBecomesLiteral(t *testing.T) {
	// An unbalanced `[` is harmless: the translator emits `\[` and the rest
	// of the pattern continues as literal characters. This means `file[abc`
	// matches the literal substring "file[abc" — surprising perhaps, but
	// it never crashes the compiler, and the user can see no matches and
	// fix their pattern.
	m, err := Compile("file[abc")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !m.Match("dir/file[abc.txt") {
		t.Error("should match the literal substring")
	}
	if m.Match("file_abc") {
		t.Error("should not match without literal [")
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		match   []string
		nomatch []string
	}{
		{
			name:    "substring lowercase smart-case",
			pattern: "status",
			match:   []string{"internal/git/status.go", "STATUS.md", "src/Status.go"},
			nomatch: []string{"tree.go", "internal/ui/model.go"},
		},
		{
			name:    "substring uppercase case-sensitive",
			pattern: "STATUS",
			match:   []string{"STATUS.md", "FOO/STATUS"},
			nomatch: []string{"internal/git/status.go", "Status.go"},
		},
		{
			name:    "glob star segment-bounded",
			pattern: "*.go",
			match:   []string{"tree.go", "pkg/util.go", "Foo.GO"},
			nomatch: []string{"README.md", "go.mod"},
		},
		{
			name:    "glob star case-sensitive when uppercase present",
			pattern: "*.GO",
			match:   []string{"pkg/UTIL.GO", "BAR.GO"},
			nomatch: []string{"pkg/util.go", "tree.go"},
		},
		{
			name:    "glob star inside path",
			pattern: "internal/g*",
			match:   []string{"internal/git/status.go", "internal/git"},
			nomatch: []string{"internal/ui/model.go", "cmd/foo.go"},
		},
		{
			name:    "double-star crosses separators",
			pattern: "**/foo.go",
			match:   []string{"a/b/foo.go", "deeply/nested/path/foo.go"},
			nomatch: []string{"bar.go", "foo.txt"},
		},
		{
			name:    "question mark single non-slash",
			pattern: "te?t.go",
			match:   []string{"test.go", "text.go"},
			nomatch: []string{"toast.go", "te/t.go"},
		},
		{
			name:    "regex prefix anchored",
			pattern: `re:^cmd/.*\.go`,
			match:   []string{"cmd/foo.go", "cmd/sub/bar.go"},
			nomatch: []string{"internal/cmd/foo.go", "lib/cmd/baz.go"},
		},
		{
			name:    "POSIX bracket negation case-sensitive",
			pattern: "[!a-z]*.md",
			match:   []string{"README.md", "FOO.md"},
			nomatch: []string{"readme.md", "foo.md"},
		},
		{
			name:    "regex prefix smart-case still applies to lowercase body",
			pattern: "re:status",
			match:   []string{"STATUS.md", "internal/git/status.go"},
			nomatch: []string{"tree.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Compile(tc.pattern)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.pattern, err)
			}
			if m == nil {
				t.Fatalf("compile %q returned nil matcher", tc.pattern)
			}
			for _, s := range tc.match {
				if !m.Match(s) {
					t.Errorf("%q should match %q", tc.pattern, s)
				}
			}
			for _, s := range tc.nomatch {
				if m.Match(s) {
					t.Errorf("%q should not match %q", tc.pattern, s)
				}
			}
		})
	}
}

func TestGlobTranslation_PreservesCharClass(t *testing.T) {
	m, err := Compile("[abc]oo.go")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, s := range []string{"aoo.go", "boo.go", "coo.go"} {
		if !m.Match(s) {
			t.Errorf("should match %q", s)
		}
	}
	if m.Match("doo.go") {
		t.Errorf("should not match doo.go")
	}
}
