package tui

import (
	"time"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
)

// healthFreshWindow is how recently a check must have succeeded for a
// node to read as healthy. Older snapshots stay true but go yellow:
// the fleet shows what mymo knows, and how stale that knowledge is.
const healthFreshWindow = 24 * time.Hour

// healthClass is the honest state of a node's knowledge: what mymo
// last saw, not a guess about the server itself.
type healthClass int

const (
	hUnchecked healthClass = iota // never probed
	hFresh                        // checked recently, succeeded
	hStale                        // succeeded, but a while ago
	hFailed                       // the last attempt failed
)

// classifyNode derives a node's health from persisted state.
func classifyNode(n domain.Node) healthClass {
	switch {
	case n.LastCheck.At.IsZero():
		return hUnchecked
	case n.LastCheck.Error != "":
		return hFailed
	case time.Since(n.LastCheck.At) <= healthFreshWindow:
		return hFresh
	default:
		return hStale
	}
}

// healthDot renders the fleet's dot for a class.
func healthDot(c healthClass) string {
	switch c {
	case hFresh:
		return okStyle.Render("●")
	case hStale:
		return warnStyle.Render("●")
	case hFailed:
		return errStyle.Render("●")
	default:
		return faintStyle.Render("●")
	}
}

// healthLabel renders the human phrase for a node's last check: what
// happened, and how long ago.
func healthLabel(n domain.Node) (string, lipgloss.Style) {
	switch classifyNode(n) {
	case hUnchecked:
		return "unchecked", faintStyle
	case hFailed:
		return "failed " + facts.FormatAge(n.LastCheck.At), errStyle
	case hStale:
		return "checked " + facts.FormatAge(n.LastCheck.At), warnStyle
	default:
		return "checked " + facts.FormatAge(n.LastCheck.At), okStyle
	}
}

// nodeStatusLine renders the dot, name, mode badge, and health label
// that head the node screen.
func nodeStatusLine(n domain.Node, badge string) string {
	label, labelStyle := healthLabel(n)
	return healthDot(classifyNode(n)) + " " + titleStyle.Render(n.Name) +
		" " + badge + "  " + faintStyle.Render("·") + " " +
		labelStyle.Render(label)
}
