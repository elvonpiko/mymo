package tui

import "charm.land/lipgloss/v2"

// Palette: Catppuccin Mocha — the palette the Charm ecosystem's huh ships as
// its default theme — giving mymo its purple / teal / green character:
// mauve for the brand, teal for accents, green for healthy states.
const (
	colMauve    = "#cba6f7"
	colLavender = "#b4befe"
	colSky      = "#89dceb"
	colTeal     = "#94e2d5"
	colGreen    = "#a6e3a1"
	colYellow   = "#f9e2af"
	colRed      = "#f38ba8"
	colText     = "#cdd6f4"
	colSubtext  = "#a6adc8"
	colFaint    = "#6c7086"
	colSurface  = "#313244"
)

var (
	brandStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colMauve))
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colText))
	textStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colText))
	subtextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colSubtext))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colSubtext))
	faintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colFaint))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colGreen))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colYellow))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colRed))
	accentStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colTeal))
	codeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colSky))

	itemStyle    = lipgloss.NewStyle()
	itemSelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colText))

	sectionLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colMauve))

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colSurface)).
			Padding(1, 3)

	factsLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colFaint))
)
