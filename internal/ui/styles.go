package ui

import "github.com/charmbracelet/lipgloss"

// The palette leans on the 256-colour cube so it renders the same on any
// reasonably modern terminal, and degrades gracefully on 16-colour ones.
var (
	colAccent   = lipgloss.AdaptiveColor{Light: "27", Dark: "39"}  // headings, borders
	colAccent2  = lipgloss.AdaptiveColor{Light: "92", Dark: "141"} // secondary accent
	colText     = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colMuted    = lipgloss.AdaptiveColor{Light: "244", Dark: "245"}
	colFaint    = lipgloss.AdaptiveColor{Light: "250", Dark: "240"}
	colOK       = lipgloss.AdaptiveColor{Light: "28", Dark: "78"}
	colWarn     = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	colDanger   = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	colMark     = lipgloss.AdaptiveColor{Light: "127", Dark: "213"}
	colSelectBg = lipgloss.AdaptiveColor{Light: "153", Dark: "24"}
)

var (
	// Chrome.
	styleTitle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleKey   = lipgloss.NewStyle().Foreground(colAccent2).Bold(true)
	styleLabel = lipgloss.NewStyle().Foreground(colMuted)
	styleValue = lipgloss.NewStyle().Foreground(colText)
	styleLogo  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	// Table.
	styleColumn   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleRow      = lipgloss.NewStyle().Foreground(colText)
	styleRowFaint = lipgloss.NewStyle().Foreground(colFaint)
	styleSelected = lipgloss.NewStyle().Foreground(colText).Background(colSelectBg).Bold(true)
	styleMarked   = lipgloss.NewStyle().Foreground(colMark).Bold(true)

	// Status.
	styleOK     = lipgloss.NewStyle().Foreground(colOK)
	styleWarn   = lipgloss.NewStyle().Foreground(colWarn)
	styleDanger = lipgloss.NewStyle().Foreground(colDanger).Bold(true)
	styleMutedT = lipgloss.NewStyle().Foreground(colMuted)

	// Boxes.
	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent)
	styleDialog = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDanger).
			Padding(1, 3)
	styleDialogTitle = lipgloss.NewStyle().Foreground(colDanger).Bold(true)
	stylePrompt      = lipgloss.NewStyle().Foreground(colAccent2).Bold(true)
)

// asciiLogo is drawn in the top-right corner, k9s style. It is dropped when
// the terminal is too narrow to carry it.
var asciiLogo = []string{
	` ___  ___ __  __ `,
	`|   \| _ \  \/  |`,
	`| |) |   / |\/| |`,
	`|___/|_|_\_|  |_|`,
}
