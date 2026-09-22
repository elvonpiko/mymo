package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/state"
	"github.com/elvonpiko/mymo/internal/version"
)

// nodeAppsView renders the applications screen for one node.
func (m Model) nodeAppsView(node string) string {
	return m.infoView("Applications",
		[]string{
			node + " has no mymo-managed applications yet.",
			"",
			"Deployment arrives with the deployment phase:",
			"mymo will detect Dockerfiles and compose projects,",
			"gate every release behind health checks,",
			"and keep the previous release for rollback.",
		},
		"[esc] back")
}

// allAppsView renders the fleet-wide applications screen.
func (m Model) allAppsView() string {
	return m.infoView("Applications",
		[]string{
			"No applications are managed yet.",
			"",
			"Deployed applications will appear here",
			"with their node, release, domain, and health.",
		},
		"[esc] back")
}

// deployView renders the deploy workflow entry point and its stages.
func (m Model) deployView() string {
	inner := titleStyle.Render("Deploy") + "\n\n" +
		"Every deployment follows the same short path:\n\n" +
		"  1. Detect      find a Dockerfile, image, or compose project\n" +
		"  2. Target      choose a node\n" +
		"  3. Configure   exposure, domain, health checks\n" +
		"  4. Review      see every change before it happens\n" +
		"  5. Execute     build, health-gate, activate — keep the old release\n\n" +
		faintStyle.Render("Local project detection arrives with the deployment phase.") + "\n" +
		faintStyle.Render("Until then, nothing on your servers changes without your hand.")
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panelStyle.Render(inner))
}

// settingsView renders settings: about, security, and the intro replay.
func (m Model) settingsView() string {
	stateDir := "~/.mymo"
	if m.store != nil {
		stateDir = m.store.Dir()
	}
	nodes := fmt.Sprintf("%d", len(m.nodes))
	inner := titleStyle.Render("Settings") + "\n\n" +
		factsLabelStyle.Width(10).Render("about") + textStyle.Render("mymo "+version.Version+" · schema "+fmt.Sprint(state.SchemaVersion)) + "\n" +
		factsLabelStyle.Width(10).Render("") + textStyle.Render(stateDir) + "\n" +
		factsLabelStyle.Width(10).Render("nodes") + textStyle.Render(nodes+" in fleet") + "\n" +
		factsLabelStyle.Width(10).Render("security") + textStyle.Render("state files 0600 · directory 0700") + "\n" +
		factsLabelStyle.Width(10).Render("") + textStyle.Render("no passwords or key material stored") + "\n\n" +
		accentStyle.Render("[i]") + textStyle.Render(" replay the first-run intro") + "    " +
		accentStyle.Render("[esc]") + textStyle.Render(" back")
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panelStyle.Render(inner))
}

// infoView renders a centered informational panel with a hint line.
func (m Model) infoView(title string, lines []string, hint string) string {
	inner := titleStyle.Render(title) + "\n\n" + strings.Join(lines, "\n")
	if hint != "" {
		inner += "\n\n" + faintStyle.Render(hint)
	}
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panelStyle.Render(inner))
}
