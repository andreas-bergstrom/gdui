package ui

import "github.com/charmbracelet/lipgloss"

var (
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Bold(true)
	dirStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#bb9af7")).Bold(true)
	fileStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5"))
	cursorStyle = lipgloss.NewStyle().Background(lipgloss.Color("#3b4261"))
	addsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a"))
	delsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
	toastStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")).Bold(true)
	kindStyle   = map[byte]lipgloss.Style{
		'M': lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68")),
		'A': lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")),
		'D': lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")),
		'R': lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")),
		'?': lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89")),
	}
)
