package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// preflightView renders the audit as the first page of initial
// setup: one row per finding, the verdict as a sentence, and an
// honest note that this page changes nothing, ever.
func (m Model) preflightView() string {
	w := m.contentWidth - 8 // the card's frame eats 8 columns
	inner := titleStyle.Render("Initial setup") + " " +
		faintStyle.Render("\u00b7 the check \u00b7 baseline "+baseline.Version) + "\n\n"

	for _, c := range m.pfChecks {
		inner += m.pfRow(c, w-17)
	}

	// every verdict sentence fits one line at the floor — wrapPlain
	// is for plain text, and the verdict carries its color
	inner += "\n" + pfVerdictLine(m.pfVerdict) + "\n"
	switch m.pfVerdict {
	case preflight.Pass, preflight.Adopt:
		inner += "\n" + faintStyle.Render("read-only — nothing on the box changes \u00b7 enter to see the plan")
	case preflight.Decide:
		inner += "\n" + faintStyle.Render("read-only \u00b7 the findings above need your call — resolve them, then check again")
	default:
		inner += "\n" + faintStyle.Render("read-only \u00b7 this box cannot become a mymo node as it stands")
	}
	return m.wizardPanelFit(inner)
}

// pfRow renders one check: verdict glyph, title, and detail, wrapping
// long details under the title so honesty is never clipped away.
func (m Model) pfRow(c preflight.Check, detailW int) string {
	var glyph string
	var g lipgloss.Style
	switch c.Outcome {
	case preflight.Pass:
		glyph, g = "\u2713", okStyle
	case preflight.Adopt:
		glyph, g = "\u2713", adoptStyle
	case preflight.Decide:
		glyph, g = "!", warnStyle
	default:
		glyph, g = "\u2717", errStyle
	}

	// glyph, space, 14-wide label, space — the detail starts here and
	// continuation lines line up under it
	indent := 17
	label := factsLabelStyle.Width(14).Render(c.Title)
	var b strings.Builder
	b.WriteString(g.Render(glyph) + " " + label + " ")
	for i, line := range wrapPlain(c.Detail, detailW) {
		if i > 0 {
			b.WriteString("\n" + strings.Repeat(" ", indent))
		}
		b.WriteString(textStyle.Render(line))
	}
	b.WriteString("\n")
	return b.String()
}

// pfVerdictLine renders the verdict as one plain sentence in its
// color: what mymo concluded, in the words the operator would use.
func pfVerdictLine(v preflight.Outcome) string {
	style, word := okStyle, "everything mymo needs is already here"
	switch v {
	case preflight.Adopt:
		style, word = adoptStyle, "mymo recognizes its own work here — the plan keeps what exists"
	case preflight.Decide:
		style, word = warnStyle, "a few findings need your call before anything is planned"
	case preflight.Abort:
		style, word = errStyle, "this box cannot become a mymo node as it stands"
	}
	return style.Render(word)
}

// wizardPanelFit is the wizard card: same border and side padding as
// the dialogs, with the page's own air rows providing the vertical
// breathing — so a floor terminal keeps every step. A card that
// would not fit renders full-width instead: a step is never clipped
// to keep a border.
func (m Model) wizardPanelFit(inner string) string {
	panel := wizardPanelStyle.Render(inner)
	if lipgloss.Height(panel) > m.contentHeight {
		return fitHeight(inner, m.contentHeight)
	}
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panel)
}

// wrapPlain wraps plain text at word boundaries to width w.
func wrapPlain(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		remaining := para
		for lipgloss.Width(remaining) > w {
			cut := w
			for cut > 0 && remaining[cut-1] != ' ' {
				cut--
			}
			if cut == 0 {
				cut = w
			}
			lines = append(lines, strings.TrimRight(remaining[:cut], " "))
			remaining = strings.TrimLeft(remaining[cut:], " ")
		}
		lines = append(lines, remaining)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
