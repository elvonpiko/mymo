package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

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

	// frameStyle is mymo's signature: the workspace canvas is always drawn
	// inside a mauve border, so the app is recognizable in any terminal.
	frameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colMauve))

	// dividerStyle separates the sticky header from the working canvas.
	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colFaint))

	// panelStyle is a quiet information card for static content.
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colSurface)).
			Padding(1, 3)

	// cardStyle is an interactive card: forms, reviews, confirmations —
	// the lavender edge marks surfaces the user acts on, against the
	// quieter surface-edged info panels.
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colLavender)).
			Padding(1, 3)

	// dangerCardStyle marks destructive confirmations in red.
	dangerCardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colRed)).
			Padding(1, 3)

	factsLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colFaint))

	// factsPanelStyle is a compact facts card: tighter than panelStyle so
	// node screens keep all actions visible on short terminals.
	factsPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colSurface)).
			Padding(0, 1)
)

// brandIcon is mymo's mark: three stems of an "m", one per palette accent —
// mauve, teal, green — standing for the fleet mymo keeps in view.
func brandIcon() string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colMauve)).Render("█") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(colTeal)).Render(" █ ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(colGreen)).Render("█")
}

// HuhTheme restyles huh's built-in Catppuccin theme with mymo's signature
// accents: teal selectors, a mauve cursor, and the brand-mauve focused
// marker, so forms feel native to the workspace. The CLI's standalone
// forms use the same theme.
func HuhTheme() huh.Theme {
	accent := lipgloss.Color(colTeal)
	brand := lipgloss.Color(colMauve)
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		t := huh.ThemeCatppuccin(isDark)

		// Compact field spacing: the form lives inside a card on the
		// workspace canvas, so it must fit a standard terminal.
		t.FieldSeparator = lipgloss.NewStyle().SetString("\n")

		t.Focused.Base = t.Focused.Base.BorderForeground(brand)
		t.Focused.Card = t.Focused.Base
		t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(accent)
		t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(accent)
		t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(accent)
		t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(accent)
		t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(accent)
		t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(brand)

		t.Blurred = t.Focused
		t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
		t.Blurred.Card = t.Blurred.Base
		t.Blurred.NextIndicator = lipgloss.NewStyle()
		t.Blurred.PrevIndicator = lipgloss.NewStyle()

		return t
	})
}

// applyHelpPalette restyles a help model's default styles with mymo's
// palette: lavender keys, subtext descriptions, faint separators.
func applyHelpPalette(h *help.Model) {
	s := h.Styles
	s.ShortKey = s.ShortKey.Foreground(lipgloss.Color(colLavender))
	s.ShortDesc = s.ShortDesc.Foreground(lipgloss.Color(colSubtext))
	s.ShortSeparator = s.ShortSeparator.Foreground(lipgloss.Color(colFaint))
	s.Ellipsis = s.Ellipsis.Foreground(lipgloss.Color(colFaint))
	s.FullKey = s.FullKey.Foreground(lipgloss.Color(colLavender))
	s.FullDesc = s.FullDesc.Foreground(lipgloss.Color(colSubtext))
	s.FullSeparator = s.FullSeparator.Foreground(lipgloss.Color(colFaint))
	h.Styles = s
}
