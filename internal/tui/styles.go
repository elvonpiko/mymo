package tui

import "charm.land/lipgloss/v2"

var (
	appStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	titleStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	appHostStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))

	itemStyle    = lipgloss.NewStyle()
	itemSelStyle = lipgloss.NewStyle().Bold(true)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 2)
)
