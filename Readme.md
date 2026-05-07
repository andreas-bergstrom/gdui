# gdui

A focused terminal UI for browsing your working-tree git diff as a sparse, collapsible file tree with inline syntax-highlighted diffs. Built to live in a narrow sidebar pane next to an agent like Claude Code, so you can see at a glance what's been changed without context-switching to a pager.

```
 gdui  /Users/you/proj
▾ lib/                    +12 -3
  ▾ M util.go             +12 -3
    @@ -1,5 +1,14 @@
     package lib
    +
    +func Sub(a, b int) int {
    +    return a - b
    +}
     ...
▸ src/                    +9  -2
▸ ? NEW.txt               +1  -0
▸ D readme.md             +0  -2
```

## Features

- **Sparse tree** — only changed paths appear; single-child directory chains are collapsed (`src/foo/bar/`).
- **Inline expansion** — press <kbd>enter</kbd> on a file to reveal the syntax-highlighted unified diff under the row, no pager handoff.
- **Three view modes** — cycle with <kbd>a</kbd> between *changed only*, *all tracked files*, and *commit log*; pick a commit to drill into its file tree.
- **Live updates** — fsnotify-backed file watcher refreshes the tree on disk changes (200 ms debounce), and refreshes the log view when a new commit lands. Useful when an agent is editing files or committing in another pane.
- **Keyboard + mouse** — vim-style navigation; click rows to toggle, scroll-wheel to scroll.
- **Status-aware markers** — `M` modified, `A` added, `D` deleted, `R` renamed, `?` untracked.
- **Single static binary** — shells out to the `git` CLI; no library needed.

## Built with

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI runtime
- [Bubbles](https://github.com/charmbracelet/bubbles) — viewport component
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) — styling
- [bubblezone](https://github.com/lrstanley/bubblezone) — mouse hit-testing for tree rows
- [Chroma](https://github.com/alecthomas/chroma) — syntax highlighting
- [fsnotify](https://github.com/fsnotify/fsnotify) — recursive file watching

## Install

```sh
git clone <repo> && cd ui
go build -o gdui .
# optionally
ln -s "$PWD/gdui" /usr/local/bin/gdui
```

Requires Go 1.23+ and `git` on `PATH`.

## Usage

Run from anywhere inside a git repo:

```sh
gdui
```

The binary is named `gdui` rather than `gd` because `gd` is a common alias for `git diff`.

### Keys

| Key                          | Action                                |
|------------------------------|---------------------------------------|
| `j` / `↓`, `k` / `↑`         | move cursor                           |
| `enter` / `space`            | toggle expand/collapse (or open commit in log mode) |
| `h` / `←`                    | collapse (or jump to parent)          |
| `l` / `→`                    | expand                                |
| `[` / `]`                    | previous / next folder                |
| `g` / `G`                    | top / bottom                          |
| `ctrl+u` / `ctrl+d`          | page up / down                        |
| `a`                          | cycle view: changed → all → log       |
| `b`                          | file history (on a file row)          |
| `esc` / `backspace`          | back out of a commit, or out of file history |
| `r`                          | refresh manually                      |
| `?`                          | toggle help                           |
| `q` / `ctrl+c`               | quit                                  |
| left-click row               | toggle expand/collapse                |
| scroll wheel                 | scroll viewport                       |

### View modes

Press <kbd>a</kbd> to cycle:

1. **Changed** *(default)* — sparse tree of files with working-tree changes vs `HEAD`.
2. **All** — every tracked file in the repo, with diff counts on changed ones.
3. **Log** — the last 100 commits on the current branch. Select one with <kbd>enter</kbd> to open its file tree (commit vs parent; root and merge commits handled). <kbd>esc</kbd> / <kbd>backspace</kbd> returns to the log.

From *changed* or *all*, press <kbd>b</kbd> on any file row to open a **file history** view — the commits that touched that file (renames followed via `git log --follow`). <kbd>enter</kbd> drills into a commit; <kbd>esc</kbd> returns to the file history, then again to the tree.

## What it shows

- Working tree compared against `HEAD` — staged changes, unstaged changes, and untracked files all in one view.
- Per-file `+N -M` line counts; folders show aggregate counts of their changed children.
- Renames are shown under the new path with `← old/path` annotation.
- Binary files render a `⟨binary file⟩` placeholder.
- Files larger than ~2000 changed lines render a truncation placeholder rather than blocking the UI.

## Development

```sh
go test ./...                              # all tests
go test ./internal/git/...                 # diff parser tests
go vet ./...
go build -o gdui .
```

The UI smoke test (`internal/ui/smoke_test.go`) auto-skips if `/tmp/gd-smoke` (or `$GD_SMOKE_REPO`) is not a git repo. To exercise it, populate that directory with a dirty repo first.

See `CLAUDE.md` for the architecture overview.

## Limitations

Out of scope for now (intentional):

- Configurable diff base — only working tree vs `HEAD` is supported. No branch / commit comparison.
- Hunk staging — this is a viewer, not `git add -p`.
- Side-by-side (split) diff view.
- Search / filter by filename.
