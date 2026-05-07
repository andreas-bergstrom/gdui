package ui

import "github.com/charmbracelet/bubbles/key"

type keymap struct {
	Up, Down, PgUp, PgDn, Top, Bottom key.Binding
	NextDir, PrevDir                  key.Binding
	Left, Right, Toggle               key.Binding
	Refresh, Help, Quit               key.Binding
	ToggleAll                         key.Binding
	Back                              key.Binding
}

var keys = keymap{
	Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	PgUp:    key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "page up")),
	PgDn:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "page down")),
	NextDir: key.NewBinding(key.WithKeys("]", "shift+down", "alt+down"), key.WithHelp("]", "next folder")),
	PrevDir: key.NewBinding(key.WithKeys("[", "shift+up", "alt+up"), key.WithHelp("[", "prev folder")),
	Top:     key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
	Bottom:  key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
	Left:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "collapse")),
	Right:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "expand")),
	Toggle:  key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("⏎/␣", "toggle")),
	Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	ToggleAll: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "changed/all/log")),
	Back:      key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
}
