package tui

import (
	"fmt"
	"strings"

	"github.com/elvonpiko/mymo/internal/baseline"
)

// planView renders the drafted change list: one line per step, the
// gates that guard the steps that need them, and the honest note that
// the confirm gate ships with apply. The full file contents and
// commands live in the CLI's plan print and in the stored step data —
// the screen is the review surface, not the storage.
func (m Model) planView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Plan") + " " +
		faintStyle.Render("\u00b7 "+m.selNode.Name+" \u00b7 baseline "+baseline.Version+" \u00b7 "+fmt.Sprint(len(m.planSteps))+" steps") + "\n\n")

	for i, s := range m.planSteps {
		b.WriteString(subtextStyle.Render(fmt.Sprintf("%2d.", i+1)) + " " +
			factsLabelStyle.Width(18).Render(s.Title) + " " +
			textStyle.Render(s.Detail) + "\n")
	}

	var gates []string
	for _, s := range m.planSteps {
		if s.Gate != "" {
			gates = append(gates, s.Gate)
		}
	}
	if len(gates) > 0 {
		b.WriteString("\n" + warnStyle.Render("gate") + " " +
			subtextStyle.Render(strings.Join(gates, "; ")))
	}

	b.WriteString("\n\n" + faintStyle.Render("awaiting your confirmation \u2014 apply ships with the next phase") + "\n")
	b.WriteString(faintStyle.Render("this plan changes nothing yet"))
	return fitHeight(b.String(), m.contentHeight)
}
