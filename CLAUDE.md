# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`gdui` — a Go + Bubble Tea TUI that renders the working-tree diff vs `HEAD` as a sparse, collapsible file tree with inline syntax-highlighted diffs. Designed to live in a narrow sidebar pane next to an agent like Claude Code. Single static binary; shells out to the `git` CLI rather than using a Go git library.

## Common commands

```sh
go build -o gdui .          # produce the binary
go test ./...               # all tests (parser unit + UI smoke)
go test ./internal/git/...  # parser tests only
go test -run TestSmoke ./internal/ui/...  # UI smoke test (uses /tmp/gd-smoke if present)
go vet ./...
```

The UI smoke test (`internal/ui/smoke_test.go`) skips automatically if `/tmp/gd-smoke` (or `$GD_SMOKE_REPO`) is not a git repo. To exercise it, populate that path with a dirty repo (mixed modified / deleted / untracked / renamed files) before running.

## Architecture

Layered, all code under `internal/`:

- **`internal/git`** — shells out to `git` and parses output.
  - `status.go`: `git status --porcelain=v2 -z` + `git diff --numstat HEAD` → `[]ChangedFile` with adds/dels/binary flag. Untracked-file line counts are filled by reading the file directly.
  - `diff.go`: `LoadHunks` returns `[]Hunk` for one file (tracked uses `git diff HEAD --`; untracked synthesizes all-`+`; deleted uses `git show HEAD:`). `ParseUnified` is a hand-rolled unified-diff parser; edge cases (no-newline-at-eof, binary, multi-hunk, CRLF, rename, deleted) are covered by `diff_test.go`.

- **`internal/tree`** — pure data layer. `Build([]ChangedFile) *Node` produces a sparse tree (only changed paths exist), sorts dirs-first, path-collapses single-child directory chains (`src/foo/bar/`), aggregates +/- counts bottom-up, and default-expands directories. `Flatten(root)` turns current expand state into a display-order row list.

- **`internal/render`** — `Hunks(path, hunks, width)` syntax-highlights diff content via `chroma` and prepends bold green/red `+`/`-` markers. Background-tinted lines were tried and abandoned: chroma's terminal formatter emits `\x1b[0m` resets that nullify lipgloss `Background` styles. The marker-only fallback is the supported approach — do not reintroduce row backgrounds without solving the ANSI-reset rewriting problem first. Diffs over `LargeDiffThreshold` (2000 lines) render a placeholder instead of being highlighted.

- **`internal/watch`** — fsnotify-backed recursive directory watcher. Ignores `.git`, `node_modules`, `vendor`, and editor swap files; debounces bursts with a quiet-period timer (default 200ms in `main.go`); auto-adds newly created subdirectories. Calls back into `main`, which sends `ui.RefreshMsg{}` to the running tea program.

- **`internal/ui`** — the Bubble Tea program.
  - `model.go`: `Model` holds `*tree.Node` root, flattened `rows`, cursor index, `viewport.Model`, and a `*zone.Manager` from `bubblezone` for mouse hit-testing.
  - `Update` dispatches on `WindowSizeMsg`, `statusMsg` (initial + refresh), `hunksMsg` (lazy hunk load), `tea.KeyMsg`, `tea.MouseMsg`. Hunks load asynchronously via `tea.Cmd`; the goroutine returns a `hunksMsg`.
  - `View` rebuilds the row list and re-marks zones on **every** call (don't cache — when a file expands and inserts N hunk lines, downstream zone Y-coords shift and stale zones mis-route clicks). Then wraps the viewport's output in `m.zones.Scan(...)`.
  - On collapse, `Hunks` are dropped from the node — re-fetched on the next expand. Git diff is cheap; this avoids stale-data bugs after `r` (refresh).
  - `r` triggers a full status reload; expand state is preserved across reloads by path in `preserveStateInto`.

`main.go` is a thin entry point: resolve repo root via `git rev-parse --show-toplevel`, then `tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())`.

## Conventions

- All git interaction goes through `os/exec`. Never add `go-git` — handling worktrees, submodules, and user config is exactly what the `git` CLI does for free.
- Pass `-c core.quotepath=false` and `-z` to status/diff invocations so paths are NUL-separated and not octal-escaped.
- The diff parser is intentionally hand-rolled and small; if you change it, update `diff_test.go` golden cases. Anchor on the `^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@` regex.
- Mouse: only left-click and wheel events are handled. `tea.MouseActionPress` + `MouseButtonLeft` toggles the row; `MouseButtonWheelUp/Down` scrolls the viewport by 3 lines.

## Naming note

The binary is named `gdui` (not `gd`) because `gd` collides with the common `git diff` shell alias.
