// Package drop parses bracketed-paste payloads produced by terminal emulators
// when a user drags a file onto the terminal window. The output of Parse is
// the list of paths the user actually dropped, or nil when the payload
// doesn't look like a drop at all.
//
// Different terminals emit different formats for the same drop:
//
//   - macOS Terminal.app: plain path, with spaces backslash-escaped.
//   - iTerm2: single-quoted path; multiple drops space-separated.
//   - WezTerm / GNOME Terminal / VTE: file:// URI with URL-encoded specials.
//   - Windows Terminal: double-quoted path.
//   - Alacritty / kitty: plain path (rarely quoted since drops there usually
//     come from Files/Finder and don't contain whitespace).
//
// Parse handles all of these. It rejects any payload where at least one
// token doesn't resolve to a regular file on disk — that's the only practical
// signal that distinguishes "user dropped a file" from "user pasted text that
// happens to contain something path-shaped". The cost is that a drop whose
// source file vanishes between drag-start and drop-complete is rejected, but
// terminals don't hand us paths for files that have already moved.
package drop

import (
	"net/url"
	"os"
	"runtime"
	"strings"
)

// Parse turns a raw bracketed-paste payload into the file paths the user
// dropped. Returns nil when the payload isn't recognizable as a drop: empty
// input, unbalanced quotes, or any token failing Stat-as-regular-file.
func Parse(payload string) []string {
	payload = strings.Trim(payload, " \t\r\n")
	if payload == "" {
		return nil
	}
	tokens := splitTokens(payload)
	if len(tokens) == 0 {
		return nil
	}
	paths := make([]string, 0, len(tokens))
	for _, t := range tokens {
		p, ok := decodeToken(t)
		if !ok || p == "" {
			return nil
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, p)
	}
	return paths
}

// splitTokens splits the payload on unescaped whitespace, respecting quotes.
// Quotes are preserved in the returned tokens — decodeToken strips them.
// On Windows the backslash-escape rule is suppressed because `\` is a path
// separator there, not an escape character; Windows terminals quote dropped
// paths anyway.
func splitTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escapeNext := false
	unixEscapes := runtime.GOOS != "windows"
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if escapeNext {
			cur.WriteRune(r)
			escapeNext = false
			continue
		}
		switch {
		case unixEscapes && r == '\\' && !inSingle:
			escapeNext = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			cur.WriteRune(r)
		case r == '"' && !inSingle:
			inDouble = !inDouble
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	if inSingle || inDouble {
		return nil
	}
	return tokens
}

// decodeToken strips surrounding matched quotes, then handles the file:// URI
// form by stripping the prefix and URL-decoding the rest. Returns the resolved
// path string and ok=false only when URI decoding fails.
func decodeToken(t string) (string, bool) {
	if len(t) >= 2 {
		first, last := t[0], t[len(t)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			t = t[1 : len(t)-1]
		}
	}
	if strings.HasPrefix(t, "file://") {
		rest := t[len("file://"):]
		decoded, err := url.PathUnescape(rest)
		if err != nil {
			return "", false
		}
		return decoded, true
	}
	return t, true
}
