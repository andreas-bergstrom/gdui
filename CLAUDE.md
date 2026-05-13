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
  - `commits.go`: `Log(repoRoot, limit)` runs `git log --pretty=format:%H%x1f%h%x1f%an%x1f%ad%x1f%s` (US-delimited, date `%ad` = `--date=short`). `CommitFiles(sha)` combines `git show --numstat -z` with `--name-status -z` (overlay accurate kinds — A/D/R) for one commit vs its parent. `CommitHunks(sha, path)` reuses `ParseUnified` on `git show <sha> -- <path>`.

- **`internal/tree`** — pure data layer. `Build([]ChangedFile) *Node` produces a sparse tree (only changed paths exist), sorts dirs-first, path-collapses single-child directory chains (`src/foo/bar/`), aggregates +/- counts bottom-up, and default-expands directories. `Flatten(root)` turns current expand state into a display-order row list.

- **`internal/render`** — `Hunks(path, hunks, width)` syntax-highlights diff content via `chroma` and prepends bold green/red `+`/`-` markers. Background-tinted lines were tried and abandoned: chroma's terminal formatter emits `\x1b[0m` resets that nullify lipgloss `Background` styles. The marker-only fallback is the supported approach — do not reintroduce row backgrounds without solving the ANSI-reset rewriting problem first. Diffs over `LargeDiffThreshold` (2000 lines) render a placeholder instead of being highlighted.

- **`internal/watch`** — fsnotify-backed recursive directory watcher. Ignores `.git`, `node_modules`, `vendor`, and editor swap files; debounces bursts with a quiet-period timer (default 200ms in `main.go`); auto-adds newly created subdirectories. Calls back into `main`, which sends `ui.RefreshMsg{Root: <wt>}` to the running tea program. The HEAD-log path is resolved via `git rev-parse --git-path logs/HEAD` (relative for main worktrees, absolute for linked) and watched at directory granularity, with `shouldIgnore` allowing only that exact absolute path through — required because in linked worktrees `<root>/.git` is a file, not a directory, so the legacy `Add(<root>/.git/logs)` silently failed and HEAD-event auto-refresh never fired.

- **`internal/ui`** — the Bubble Tea program.
  - `model.go`: `Model` holds a slice `sections []*WorktreeSection` (one per linked git worktree of the same repo), an `activeWT` index, a `[]displayRow` row list, cursor index, `viewport.Model`, and a `*zone.Manager` from `bubblezone` for mouse hit-testing. With one section the UI is byte-identical to the historic single-tree view (no header chrome); with two or more, each section renders under a collapsible header (`wt-N` zone) showing branch + change counts, and tree rows indent one level under their header.
  - `displayRow` is a discriminated-union interface (`headerRow{sectionIdx}` / `treeRow{sectionIdx, *tree.Node}` / `commitRow{idx}`). All row-type-aware code (cursor math, mouse, key dispatch, render) type-switches; tree rows route git operations through `m.sections[sectionIdx].WT.Root` — `m.repoRoot` is only the launch cwd's worktree, used to pick the initial active section.
  - `sections.go`: pure helpers — `flattenSections(secs, showHeaders)` builds the row list, `flattenCommitTree` does the same for `ModeCommit`, `findSectionByRoot(secs, root)` returns -1 when no section owns that root (this is the **stale-message drop**: every async msg type — `statusMsg`, `logMsg`, `hunksMsg`, `commitTreeMsg` — carries `Root string` and is dropped on `findSectionByRoot < 0`, so an in-flight message for a removed worktree never panics or misroutes).
  - Mode enum: `ModeChanged` → `ModeAll` → `ModeLog` → cycles via `a`; `ModeLog` shows commits for the **active** section only, `tab`/`shift+tab` cycles `activeWT`. Selecting a commit enters `ModeCommit` (tree of that commit's files, owned by `m.selectedRoot`); `esc`/`backspace` returns. `ModeFileLog` (entered with `b`) is also tied to the cursor's section. `RefreshMsg{Root}` from the file watcher is ignored in `ModeLog`/`ModeFileLog`/`ModeCommit` — those views aren't tied to working-tree state.
  - Hunks load asynchronously: `loadHunksCmd(root, path, file, sha)` — empty `sha` means worktree (uses `git.LoadHunks`), non-empty routes to `git.CommitHunks`. The originating `root` flows into the resulting `hunksMsg` so the model can find the right tree to attach the hunks to (different sections may share file paths).
  - `View` rebuilds the row list and re-marks zones on **every** call (don't cache — when a file expands and inserts N hunk lines, downstream zone Y-coords shift and stale zones mis-route clicks; section header collapse/expand has the same effect amplified across N sections). Then wraps the viewport's output in `m.zones.Scan(...)`.
  - On collapse, `Hunks` are dropped from the node — re-fetched on the next expand. Git diff is cheap; this avoids stale-data bugs after `r` (refresh).
  - Same bubbletea partial-redraw bug also fires when the cursor moves between a section header and a tree row (or across sections) in multi-worktree views — the section header line's full-width bg differs in byte structure from a tree row's, and bubbletea's diff misses the repaint, leaving stale highlight from the previous position visible. `handleKeyTree` returns `tea.ClearScreen` when `needsRepaintAfterMove` detects such a transition. Same-section, same-kind navigation doesn't trigger the bug, so we don't pay the flicker for it.
  - Cursor preservation across `statusMsg` reloads is anchored on `(sectionRoot, treePath)` — see `cursorAnchor`/`restoreCursor` in `model.go`. Earlier `cursorPath`-only logic reset the cursor to row 0 whenever a refresh arrived while the cursor was on a section header (because `cursorPath()` returned `""` for headers), causing the cursor to "jump to top" during multi-worktree navigation any time a file watcher event fired. Anchor on the section root and fall back to that section's header if the file vanished.
  - `r` triggers `loadInitDataCmd` which re-lists worktrees and reloads all sections in one synchronous batch — sections that survive (matched by `WT.Root`) preserve their per-node expand state via `preserveTreeState`. `r` also invokes the `restartWatchers` callback (wired in from `main.go` via `ui.New`'s second arg), which stops the existing watcher set and respawns one watcher per current worktree + nested repo — so worktrees added or removed mid-session are picked up without restarting gdui.

`main.go`: resolves repo root via `git rev-parse --show-toplevel`, lists worktrees via `git.ListWorktrees`, and spawns one `watch.Start` goroutine per worktree — each fires `ui.RefreshMsg{Root: <its-own-root>}` so the model can route per-section reloads instead of refreshing every section on every save. Falls back to a single watcher on the launch root if the worktree list fails.

## Conventions

- All git interaction goes through `os/exec`. Never add `go-git` — handling worktrees, submodules, and user config is exactly what the `git` CLI does for free.
- Pass `-c core.quotepath=false` and `-z` to status/diff invocations so paths are NUL-separated and not octal-escaped.
- The diff parser is intentionally hand-rolled and small; if you change it, update `diff_test.go` golden cases. Anchor on the `^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@` regex.
- Mouse: only left-click and wheel events are handled. `tea.MouseActionPress` + `MouseButtonLeft` toggles the row; `MouseButtonWheelUp/Down` scrolls the viewport by 3 lines.

## Release pipeline

Releases are produced by `.github/workflows/release.yml`, triggered on `v*` tag pushes. Do **not** manually build and upload artifacts — tag the commit, push the tag, the workflow does the rest.

- `.goreleaser.yaml` is the source of truth for build matrix, archives, checksums, signing, and Homebrew formula. Modify there, not in the workflow.
- The build matrix is the `goos` × `goarch` cartesian product in `builds[0]`, with `windows/arm64` carved out via `ignore:`. To add a target, add it to the lists; to add a Linux package format (`.deb`/`.rpm`), add an `nfpms:` block — make sure the signing post-hook still no-ops on the new target.
- `main.version`, `main.commit`, `main.date` are package-level vars in `main.go` injected via `-ldflags '-X main.version=…'` at build time. The defaults (`"dev"`, `"none"`, `"unknown"`) are placeholders. Do not assign them anywhere else.
- `make build` and `make install` inject the same vars from `git describe --tags --always --dirty` so local builds know their version.
- macOS binaries are signed + notarized by `scripts/sign-darwin.sh`, wired in as a per-build `hooks.post` in `.goreleaser.yaml`. It exits 0 on non-darwin targets, on snapshot builds, and when `QUILL_SIGN_P12` is unset — so unsigned interim releases work by simply leaving the secret empty in the workflow env.
- Test `.goreleaser.yaml` changes locally before tagging: `goreleaser release --snapshot --clean --skip=publish`. Snapshot builds skip signing automatically, so no secrets or `quill` install are needed.
- Six repo-level GitHub Actions secrets back the pipeline. Never reference them in commits or in `.goreleaser.yaml` literally — the workflow forwards them as env: `QUILL_SIGN_P12`, `QUILL_SIGN_PASSWORD`, `QUILL_NOTARY_KEY`, `QUILL_NOTARY_KEY_ID`, `QUILL_NOTARY_ISSUER`, `HOMEBREW_TAP_GITHUB_TOKEN`.
- The Homebrew formula auto-publishes to a separate repo, `andreas-bergstrom/homebrew-tap`, under `Formula/gdui.rb`.
- The `brews:` block in `.goreleaser.yaml` is deprecated by GoReleaser (in favor of `homebrew_casks:`), but Casks are macOS-only and we want Linux Homebrew support too — keep `brews:` until upstream actually drops it.

## Naming note

The binary is named `gdui` (not `gd`) because `gd` collides with the common `git diff` shell alias.
