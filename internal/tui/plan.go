package tui

import (
	"fmt"
	"strings"

	"github.com/elvonpiko/mymo/internal/baseline"
)

// planView is initial setup's second and last page: the drafted
// change list — one line per step, each step's guard stated inline —
// and, in the same card, the typed name that confirms it. One page:
// what mymo will do, and the act of letting it. The full file
// contents live in the CLI's plan print; the screen is the review
// surface, not the storage.
func (m Model) planView() string {
	n := m.selNode
	w := m.contentWidth - 8 // the card's frame eats 8 columns
	var b strings.Builder
	b.WriteString(titleStyle.Render("Initial setup") + " " +
		faintStyle.Render("\u00b7 the plan \u00b7 "+n.Name+" \u00b7 baseline "+baseline.Version+" \u00b7 "+fmt.Sprint(len(m.planSteps))+" steps") + "\n\n")

	for i, s := range m.planSteps {
		for j, line := range wrapDetail(s.Detail, w-20) {
			if j == 0 {
				b.WriteString(subtextStyle.Render(fmt.Sprintf("%2d.", i+1)) + " " +
					factsLabelStyle.Width(16).Render(s.Title) + " " +
					textStyle.Render(line) + "\n")
			} else {
				b.WriteString(strings.Repeat(" ", 20) + textStyle.Render(line) + "\n")
			}
		}
		if s.Gate != "" {
			// the guard belongs to the step it guards, not to a
			// vocabulary footer: plain words, under their step
			for _, line := range wrapDetail(s.Gate, w-20) {
				b.WriteString(strings.Repeat(" ", 20) + subtextStyle.Render(line) + "\n")
			}
		}
	}

	if n.User == "root" {
		b.WriteString(strings.Repeat(" ", 4) + subtextStyle.Render("root logins end at apply — access continues as mymo + console") + "\n")
	}

	// one line says both the promise and the act: the name typed in
	// full is the confirmation, and nothing runs before it
	typed := m.applyTyped
	cursor := accentStyle.Render("\u258c")
	if typed == n.Name {
		cursor = okStyle.Render("\u258c")
	}
	b.WriteString("\n" + subtextStyle.Render("type ") + accentStyle.Render(n.Name) +
		subtextStyle.Render(" in full to apply \u2014 anything else aborts") + "\n" +
		accentStyle.Render("> ") + textStyle.Render(typed) + cursor)

	return m.wizardPanelFit(b.String())
}
