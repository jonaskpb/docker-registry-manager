package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the terminal UI and blocks until the user quits.
func Run(cfg Config) error {
	// No mouse capture: grabbing the mouse would break the terminal's own
	// text selection, which is how people copy things out of a TUI.
	p := tea.NewProgram(New(cfg), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
