package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreas-bergstrom/gdui/internal/git"
	"github.com/andreas-bergstrom/gdui/internal/ui"
	"github.com/andreas-bergstrom/gdui/internal/watch"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("gdui %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gd: not a git repository (or no git in PATH):", err)
		os.Exit(1)
	}

	m := ui.New(root)
	// Bracketed paste is on by default in Bubble Tea v1.3+; the drag-drop
	// import flow in internal/ui/drop.go relies on it (paste arrives as a
	// KeyMsg with msg.Paste == true). Don't add tea.WithoutBracketedPaste()
	// without removing the drop handler first.
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Spawn one file watcher per linked worktree so HEAD-event auto-refresh
	// works regardless of which worktree was edited. List once at startup —
	// worktrees added/removed mid-session require restarting gdui.
	stops := startWatchers(root, prog)
	defer func() {
		for _, s := range stops {
			s()
		}
	}()

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gd:", err)
		os.Exit(1)
	}
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func startWatchers(root string, prog *tea.Program) []func() {
	wts, err := git.ListWorktrees(root)
	if err != nil || len(wts) == 0 {
		// Fall back to watching just the launch worktree.
		stop := watch.Start(root, 200*time.Millisecond, func() {
			prog.Send(ui.RefreshMsg{Root: root})
		})
		return []func(){stop}
	}
	// Append nested repos (independent or submodules) discovered under each
	// known worktree so they get their own watcher and refresh in isolation.
	// Watchers ignore events outside their own root (internal/watch/watch.go
	// shouldIgnore), so the parent watcher will not fire for writes inside a
	// nested repo — those route only to that nested repo's section.
	all := append([]git.Worktree(nil), wts...)
	all = append(all, git.DiscoverNestedReposRecursive(wts, 0)...)
	stops := make([]func(), 0, len(all))
	for _, wt := range all {
		stop := watch.Start(wt.Root, 200*time.Millisecond, func() {
			prog.Send(ui.RefreshMsg{Root: wt.Root})
		})
		stops = append(stops, stop)
	}
	return stops
}
