package ui

import "github.com/charmbracelet/lipgloss"

// visibleWidth measures a rendered line in terminal cells, ignoring the ANSI
// styling around it.
func visibleWidth(s string) int { return lipgloss.Width(s) }
