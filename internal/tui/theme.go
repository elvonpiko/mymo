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
	colBase     = "#1e1e2e"
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

	// panelStyle is a quiet information card for static content. All
	// inner cards share this near-invisible edge: the mauve frame is
	// mymo's only loud border.
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colSurface)).
			Padding(1, 3)

	// cardStyle is an interactive card: forms, reviews, confirmations.
	// Visually identical to the info panels — the content itself shows
	// what is interactive — keeping the canvas minimal and consistent.
	cardStyle = panelStyle

	// dangerCardStyle marks destructive confirmations; the warning lives
	// in the card's text, not in its edge.
	dangerCardStyle = panelStyle

	factsLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colFaint))

	// factsPanelStyle is a compact facts card: tighter than panelStyle so
	// node screens keep all actions visible on short terminals.
	factsPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colSurface)).
			Padding(0, 1)

	// adoptStyle marks components mymo can take over: teal sits next
	// to the palette's green-ok and yellow-warn without shouting.
	adoptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colTeal))

	// titled-card chrome: the border stays as quiet as the card, while
	// the name riding it carries the one accent a card is allowed.
	cardBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colSurface))
	cardTitleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colMauve))
)

// brandIcon is mymo's mark: a small skyline of three full-block stems —
// mauve, teal, green, one per palette accent, standing for the fleet
// mymo keeps in view — linked by lavender half-block steps.
func brandIcon() string {
	stem := func(c string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("█")
	}
	step := lipgloss.NewStyle().Foreground(lipgloss.Color(colLavender)).Render("▀")
	return stem(colMauve) + " " + step + " " +
		stem(colTeal) + " " + step + stem(colGreen)
}

// HuhTheme restyles huh's built-in Catppuccin theme with mymo's
// signature accents so forms feel native to the workspace: teal
// selectors, a mauve cursor, and a thin mauve bar marking the focused
// field — never a thick border, which belongs to the frame alone.
// Titles, descriptions, placeholders and help match the panel palette.
// The CLI's standalone forms use the same theme.
func HuhTheme() huh.Theme {
	accent := lipgloss.Color(colTeal)
	brand := lipgloss.Color(colMauve)
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		t := huh.ThemeCatppuccin(isDark)

		// Compact field spacing: the form lives inside a card on the
		// workspace canvas, so it must fit a standard terminal.
		t.FieldSeparator = lipgloss.NewStyle().SetString("\n")

		// A thin brand-colored bar marks the focused field; blurred
		// fields carry no edge at all. BorderStyle + BorderLeft(true)
		// enables only the left edge — Border() would add empty rows.
		t.Focused.Base = lipgloss.NewStyle().
			PaddingLeft(1).
			BorderStyle(lipgloss.Border{Left: "│"}).
			BorderLeft(true).
			BorderForeground(brand)
		t.Focused.Card = t.Focused.Base

		// Field titles read like panel headings: bold text color when
		// focused, plain when blurred.
		t.Focused.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colText))

		t.Focused.Description = t.Focused.Description.Foreground(lipgloss.Color(colSubtext))
		t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(accent)
		t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(accent)
		t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(accent)
		t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(accent)
		t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(accent)
		t.Focused.TextInput.Text = t.Focused.TextInput.Text.Foreground(lipgloss.Color(colText))
		t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(lipgloss.Color(colFaint))
		t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(brand)

		// Confirm buttons (CLI `node rm`): brand fill for the active
		// choice, quiet surface for the other.
		t.Focused.FocusedButton = t.Focused.FocusedButton.
			Foreground(lipgloss.Color(colBase)).Background(brand)
		t.Focused.BlurredButton = t.Focused.BlurredButton.
			Foreground(lipgloss.Color(colText)).Background(lipgloss.Color(colSurface))

		// In-form help matches the workspace footer's palette.
		t.Help.ShortKey = t.Help.ShortKey.Foreground(lipgloss.Color(colLavender))
		t.Help.ShortDesc = t.Help.ShortDesc.Foreground(lipgloss.Color(colSubtext))
		t.Help.ShortSeparator = t.Help.ShortSeparator.Foreground(lipgloss.Color(colFaint))
		t.Help.Ellipsis = t.Help.Ellipsis.Foreground(lipgloss.Color(colFaint))
		t.Help.FullKey = t.Help.FullKey.Foreground(lipgloss.Color(colLavender))
		t.Help.FullDesc = t.Help.FullDesc.Foreground(lipgloss.Color(colSubtext))
		t.Help.FullSeparator = t.Help.FullSeparator.Foreground(lipgloss.Color(colFaint))

		t.Blurred = t.Focused
		t.Blurred.Title = lipgloss.NewStyle().Foreground(lipgloss.Color(colText))
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
