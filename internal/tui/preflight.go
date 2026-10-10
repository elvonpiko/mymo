package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// preflightView renders the audit as the first page of initial
// setup: one row per finding, the verdict as a sentence, and an
// honest note that this page changes nothing, ever. When the
// findings outnumber the card, the rows walk under the verdict with
// j/k — the verdict and the way forward are pinned, because a page
// that clips its own conclusion is a page that lies.
func (m Model) preflightView() string {
	head := titleStyle.Render("Initial setup") + " " +
		faintStyle.Render("\u00b7 the check \u00b7 baseline "+baseline.Version) + "\n\n"

	rows, rowCount := m.pfRowsText()

	scrollable := rowCount > 0 && m.pfScrollMax(rowCount) > 0

	// every verdict sentence fits one line at the floor — wrapPlain
	// is for plain text, and the verdict carries its color
	tail := "\n" + pfVerdictLine(m.pfVerdict) + "\n"
	switch m.pfVerdict {
	case preflight.Pass, preflight.Adopt:
		tail += "\n" + faintStyle.Render("read-only — nothing on the box changes \u00b7 enter to see the plan")
	case preflight.Decide:
		if scrollable {
			tail += "\n" + faintStyle.Render("read-only \u00b7 follow the fixes above \u00b7 j/k scroll")
		} else {
			tail += "\n" + faintStyle.Render("read-only \u00b7 follow the fixes above, then check again")
		}
	default:
		if scrollable {
			tail += "\n" + faintStyle.Render("read-only \u00b7 the fixes above say what would have to change \u00b7 j/k scroll")
		} else {
			tail += "\n" + faintStyle.Render("read-only \u00b7 this box cannot become a mymo node as it stands — the fixes above say what would have to change")
		}
	}

	inner := head
	if scrollable {
		// the card holds its shape and the finding keeps its whole
		// story: the window never cuts a row in half, so what the
		// eye reads is what there is
		groups := m.pfGroups()
		window := m.pfWindowRows()
		taken := 0
		var b strings.Builder
		for i := m.pfScroll; i < len(groups) && taken+len(groups[i]) <= window; i++ {
			if taken > 0 {
				b.WriteString("\n")
			}
			for j, line := range groups[i] {
				if j > 0 {
					b.WriteString("\n")
				}
				b.WriteString(line)
			}
			taken += len(groups[i])
		}
		if taken > 0 {
			inner += b.String() + "\n"
		}
	} else {
		inner += rows
	}
	inner += tail
	return m.wizardPanelFit(inner)
}

// pfGroups wraps every finding as its own block of lines: a row and
// its fix belong together, and the window cuts only between them.
func (m Model) pfGroups() [][]string {
	w := m.contentWidth - 8
	groups := make([][]string, 0, len(m.pfChecks))
	for _, c := range m.pfChecks {
		row := strings.TrimSuffix(m.pfRow(c, w-17), "\n")
		groups = append(groups, strings.Split(row, "\n"))
	}
	return groups
}

// pfRowsText builds the findings as the block that can walk, with
// the row count the keys need.
func (m Model) pfRowsText() (string, int) {
	var b strings.Builder
	for _, g := range m.pfGroups() {
		for i, line := range g {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String(), len(m.pfGroups())
}

// pfWindowRows is how many lines the findings may occupy.
func (m Model) pfWindowRows() int {
	head := titleStyle.Render("Initial setup") + " " +
		faintStyle.Render("\u00b7 the check \u00b7 baseline "+baseline.Version) + "\n\n"
	window := m.contentHeight - 2 - lipgloss.Height(head) - 2 - 2
	if window < 1 {
		window = 1
	}
	return window
}

// pfScrollMax is the last finding the window can start from:
// counted from the tail, because findings are not the same height
// and the last window is what the end of the list really looks
// like.
func (m Model) pfScrollMax(rowCount int) int {
	groups := m.pfGroups()
	window := m.pfWindowRows()
	taken, last := 0, len(groups)-1
	for i := len(groups) - 1; i >= 0; i-- {
		if taken+len(groups[i]) > window {
			break
		}
		taken += len(groups[i])
		last = i
	}
	if last < 0 {
		return 0
	}
	return last
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
	// the fix says what to do on the box so mymo can take it from
	// there — never a vague "resolve it", always the exact move
	if c.Fix != "" {
		b.WriteString("\n" + strings.Repeat(" ", indent))
		for i, line := range wrapPlain("fix \u00b7 "+c.Fix, detailW) {
			if i > 0 {
				b.WriteString("\n" + strings.Repeat(" ", indent))
			}
			b.WriteString(subtextStyle.Render(line))
		}
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
		style, word = warnStyle, "a few findings stand between this box and the plan — each names its fix"
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
