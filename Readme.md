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
- **Live updates** — fsnotify-backed file watcher refreshes the tree on disk changes (200 ms debounce). Useful when an agent is editing files in another pane.
- **Keyboard + mouse** — vim-style navigation; click rows to toggle, scroll-wheel to scroll.
- **Status-aware markers** — `M` modified, `A` added, `D` deleted, `R` renamed, `?` untracked.
- **Single static binary** — shells out to the `git` CLI; no library needed.

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
| `enter` / `space`            | toggle expand/collapse                |
| `h` / `←`                    | collapse (or jump to parent)          |
| `l` / `→`                    | expand                                |
| `g` / `G`                    | top / bottom                          |
| `ctrl+d` / `ctrl+u`          | half-page down / up                   |
| `r`                          | refresh manually                      |
| `?`                          | toggle help                           |
| `q` / `ctrl+c`               | quit                                  |
| left-click row               | toggle expand/collapse                |
| scroll wheel                 | scroll viewport                       |

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
