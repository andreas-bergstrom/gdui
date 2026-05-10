// Package filter compiles user-typed patterns into matchers used by the UI
// layer to filter the changed-files tree. It supports a hybrid syntax:
// substring by default, glob wildcards when the input contains *, ?, or [,
// and full Go regexp via the "re:" prefix.
package filter

import (
	"regexp"
	"strings"
	"unicode"
)

// Matcher reports whether a candidate string matches a compiled pattern.
// A nil *Matcher matches everything (the no-filter convenience).
type Matcher struct {
	re *regexp.Regexp
}

// Compile parses a pattern and returns a compiled Matcher.
//
// Returns (nil, nil) for an empty pattern. Returns (nil, err) for input that
// fails to compile (e.g. invalid regex body or an unbalanced bracket class).
//
// Pattern dispatch (in order):
//   - "re:"-prefix     → body compiled directly as Go regexp
//   - contains *, ?, [ → glob translated to regexp (see globToRegex)
//   - otherwise        → substring (regexp.QuoteMeta'd, unanchored)
//
// Smart-case: if the pattern (or "re:" body) contains no uppercase letter and
// no bracket character class, "(?i)" is prepended. The bracket-class
// exception preserves the meaning of POSIX-style classes like `[!a-z]`,
// where applying (?i) would expand `[^a-z]` to "not a letter at all".
func Compile(pattern string) (*Matcher, error) {
	if pattern == "" {
		return nil, nil
	}
	var body, caseSrc string
	switch {
	case strings.HasPrefix(pattern, "re:"):
		body = pattern[len("re:"):]
		caseSrc = body
	case hasGlobMeta(pattern):
		body = globToRegex(pattern)
		caseSrc = pattern
	default:
		body = regexp.QuoteMeta(pattern)
		caseSrc = pattern
	}
	re, err := regexp.Compile(caseFlags(caseSrc) + body)
	if err != nil {
		return nil, err
	}
	return &Matcher{re: re}, nil
}

// Match reports whether s matches the compiled pattern. A nil receiver
// returns true so callers can write `m.Match(s)` without a separate nil-check.
func (m *Matcher) Match(s string) bool {
	if m == nil || m.re == nil {
		return true
	}
	return m.re.MatchString(s)
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

func caseFlags(s string) string {
	if strings.Contains(s, "[") {
		return ""
	}
	for _, r := range s {
		if unicode.IsUpper(r) {
			return ""
		}
	}
	return "(?i)"
}

// globToRegex translates a glob to an unanchored regex body. Rules:
//   - **        → .*           (matches across path separators)
//   - *         → [^/]*        (matches within a path segment)
//   - ?         → [^/]         (single non-slash character)
//   - [...]     → [...]        (character class preserved)
//   - [!...]    → [^...]       (POSIX glob negation → regex negation)
//   - regex meta (. + ( ) | ^ $ \ { }) escaped
//   - everything else literal
//
// An unbalanced `[` is emitted as `\[` and a literal trailing run, which makes
// regexp.Compile surface a parseable error to the user via the status row.
func globToRegex(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '*':
			if i+1 < len(s) && s[i+1] == '*' {
				b.WriteString(".*")
				i += 2
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			end := i + 1
			if end < len(s) && (s[end] == '!' || s[end] == '^') {
				end++
			}
			if end < len(s) && s[end] == ']' {
				end++
			}
			for end < len(s) && s[end] != ']' {
				end++
			}
			if end >= len(s) {
				b.WriteString("\\[")
				i++
				continue
			}
			group := s[i+1 : end]
			if strings.HasPrefix(group, "!") {
				group = "^" + group[1:]
			}
			b.WriteByte('[')
			b.WriteString(group)
			b.WriteByte(']')
			i = end + 1
		case '.', '+', '(', ')', '|', '^', '$', '\\', '{', '}':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
