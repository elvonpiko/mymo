package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// preflightView renders the audit's verdicts: one row per check, the
// overall verdict last, and an honest note that planning and apply
// are not part of this phase — the screen changes nothing, ever.
func (m Model) preflightView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Preflight") + " " +
		faintStyle.Render("\u00b7 mymo baseline "+baseline.Version+" \u00b7 read-only audit") + "\n\n")

	for _, c := range m.pfChecks {
		b.WriteString(m.pfRow(c))
	}

	b.WriteString("\n")
	b.WriteString(pfVerdictLine(m.pfVerdict) + "\n")
	switch m.pfVerdict {
	case preflight.Pass, preflight.Adopt:
		b.WriteString(faintStyle.Render("planning the baseline changes arrives next; ") +
			faintStyle.Render("nothing on the node has changed"))
	case preflight.Decide:
		b.WriteString(faintStyle.Render("resolve the decisions above, then re-run; ") +
			faintStyle.Render("nothing on the node has changed"))
	default:
		b.WriteString(faintStyle.Render("this node is not suitable as a mymo app host; ") +
			faintStyle.Render("nothing was changed"))
	}
	return fitHeight(b.String(), m.contentHeight)
}

// pfRow renders one check: verdict glyph, title, and detail, wrapping
// long details under the title so honesty is never clipped away.
func (m Model) pfRow(c preflight.Check) string {
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
	detailW := max(m.contentWidth-indent, 30)
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

// pfVerdictLine renders the summary in the verdict's color.
func pfVerdictLine(v preflight.Outcome) string {
	style := okStyle
	word := "the path is clear"
	switch v {
	case preflight.Adopt:
		style, word = adoptStyle, "existing components can be adopted"
	case preflight.Decide:
		style, word = warnStyle, "your call is required before anything is planned"
	case preflight.Abort:
		style, word = errStyle, "this node cannot be prepared as an app host"
	}
	return style.Render("verdict: "+v.String()) + textStyle.Render(" \u2014 "+word)
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
