package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andreasbergstrom/gd/internal/ui"
	"github.com/andreasbergstrom/gd/internal/watch"
)

func main() {
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gd: not a git repository (or no git in PATH):", err)
		os.Exit(1)
	}

	m := ui.New(root)
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	stop := watch.Start(root, 200*time.Millisecond, func() {
		prog.Send(ui.RefreshMsg{})
	})
	defer stop()

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
