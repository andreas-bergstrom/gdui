# Cursor highlight rework — replace full-row bg with leading marker

## Problem

`Model.applyCursor` (`internal/ui/model.go`) wraps a styled row in
`cursorStyle.Background(...)` and pads it to viewport width:

```go
return cursorStyle.Render(row + padding)
```

The wrapped row already contains internal styled spans
(`dirStyle.Render(name)`, `dimStyle.Render(counts)`, `kindStyle[k].Render(...)`)
each of which emits a trailing `\x1b[0m` reset. Those inner resets
nullify the surrounding bg, so visually the highlight only covers the
prefix up to the first inner reset (e.g. just `▾ feat-demo` of a
section header — see screenshots in conversation history).

Worse, the cursor row's byte structure differs from non-cursor rows in
ways bubbletea's partial-redraw diff trips on: visible widths match
across frames but byte structures don't, and bubbletea misses the
repaint at certain transitions (header↔tree, cross-section). Stale
highlight from the previous cursor position stays on screen.

Current mitigation: `handleKeyTree` returns `tea.ClearScreen` on those
transitions (`needsRepaintAfterMove`). This works but causes a brief
full-screen flicker on every cross-section / row-kind navigation in
multi-worktree views. Same root issue is documented for the
diff-line-bg attempt in CLAUDE.md (chroma's `\x1b[0m` resets
nullifying lipgloss `Background`).

## Recommended fix — Option A: leading marker

Replace the bg-color cursor highlight with a 1-character marker at the
start of the cursor row:

```
▾ feat-demo  (M:1 ?:1)         ← non-cursor (current first column: chevron)
▶▾ feat-demo  (M:1 ?:1)        ← cursor (extra leading marker)
```

Or repurpose the existing leading column so non-cursor rows show a
space and cursor rows show `▶`:

```
 ▾ feat-demo  (M:1 ?:1)        ← non-cursor
▶▾ feat-demo  (M:1 ?:1)        ← cursor
```

### Why this fixes both bugs

- **Highlight visibility:** the marker is its own simple styled span.
  Inner resets in the rest of the row don't interact with it.
- **Bubbletea redraw glitch:** byte structure of cursor vs non-cursor
  differs by exactly one character at a fixed position, not by a
  full-row bg envelope. The diff has nothing exotic to mishandle, so
  the `tea.ClearScreen` mitigation can be removed (along with
  `needsRepaintAfterMove` and the `prevRow` capture in
  `handleKeyTree`).

### Scope

- `internal/ui/model.go` — `applyCursor` becomes a marker prepend, not
  a bg+pad+wrap. Padding to viewport width can also go away (the
  viewport already pads each line via lipgloss in `View()`).
- `renderTreeRow`, `renderSectionHeader`, `renderCommitRow`,
  `renderLine` (in-diff cursor in `internal/render/diff.go`) — adjust
  to leave room for the marker column.
- Indent math: tree rows currently start with `strings.Repeat("  ",
  depth)` for indentation. Marker would prepend before that, or
  reserve the first column always (gutter style).
- Remove `needsRepaintAfterMove` and the `tea.ClearScreen` return in
  `handleKeyTree` (model.go:670-681 in current code). Keep the
  `tea.ClearScreen` returns in `toggle`/`toggleNode` — those guard
  against a different shrink-related bubbletea bug.
- Update CLAUDE.md notes: drop the cross-section ClearScreen entry,
  drop the "background-tinted lines were tried and abandoned" note in
  the render section if the diff-line cursor highlight is also
  switched to marker-only (it already is — see `cursorLine` in
  `internal/render/diff.go` is bg-only and so has the same bug; the
  reworked marker approach can be uniform across both).

### Trade-offs

- Less visual prominence than a full-row bg highlight. A reverse-video
  marker (`\x1b[7m▶\x1b[0m`) or a bold colored marker can compensate.
- Costs 1 column on the left. In the narrow sidebar this matters; the
  current chevron column can be repurposed (see ASCII above) so the
  net cost is 0.
- Truncation math (`fitWidth` / `TruncateANSI`) doesn't change — still
  truncates by visible width.

## Alternative considered: Option B — SGR rewriter

Walk the cursor row's bytes; after every `\x1b[0m` (or partial-reset
SGR), re-emit the bg-set sequence so the bg survives inner resets.
Append a single trailing reset.

- **Pros:** preserves the current full-row bg UX exactly. Also
  resurrects the diff-line bg highlight that was abandoned for the
  same root cause (CLAUDE.md notes "do not reintroduce row backgrounds
  without solving the ANSI-reset rewriting problem first" — this
  solves it).
- **Cons:** custom ANSI rewriter is real work and easy to get
  wrong. Have to handle `\x1b[0m`, `\x1b[m` (implicit reset),
  `\x1b[1;31;0;...m` (compound resets), and not break things like
  hyperlinks (`\x1b]8;;...`). Tests need broad coverage.

If full-row bg is a hard UX requirement, Option B is the right call,
but Option A is simpler and removes the bubbletea workaround at the
same time.

## Open question

Is the full-row bg highlight a UX must-have, or is a leading marker
acceptable? That choice picks Option A vs. Option B.
