package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
	"github.com/elvonpiko/mymo/internal/version"
)

// nodeAppsView renders one node's applications: what is deployed
// there, from the record mymo keeps.
func (m Model) nodeAppsView(node string) string {
	return m.appsPage(m.visibleApps(screen{kind: scNodeApps, node: node}),
		node+" has no mymo-managed applications yet.",
		"deploy from a project directory: mymo deploy")
}

// allAppsView renders the fleet-wide applications screen.
func (m Model) allAppsView() string {
	return m.appsPage(m.apps,
		"No applications are managed yet.",
		"deploy from a project directory: mymo deploy")
}

// appsPage draws the applications list — one row per app: name,
// node, exposure, and which release is live. The record is the
// truth these pages show; live truth is a check away.
func (m Model) appsPage(apps []domain.App, emptyLine, emptyHint string) string {
	if len(apps) == 0 {
		return m.infoView("Applications",
			[]string{emptyLine, "", emptyHint},
			"[esc] back")
	}
	rows := make([]string, 0, len(apps))
	for i, a := range apps {
		cursor, nameStyle := "  ", itemStyle
		if i == m.appCursor {
			cursor, nameStyle = "> ", itemSelStyle
		}
		expose := a.Domain
		exposeStyle := subtextStyle
		if expose == "" {
			expose, exposeStyle = "internal", faintStyle
		}
		release, relStyle := "no releases", faintStyle
		if r, ok := a.ActiveRelease(); ok {
			release = fmt.Sprintf("r%d live", r.ID)
			relStyle = okStyle
		}
		rows = append(rows, cursor+
			nameStyle.Width(14).Render(shorten(a.Name, 14))+
			dimStyle.Width(12).Render(shorten(a.Node, 12))+
			exposeStyle.Width(24).Render(shorten(expose, 24))+
			relStyle.Render(release))
	}
	inner := strings.Join(rows, "\n")
	// the canvas opens with air, like every page
	return fitHeight("\n"+titledCard("Applications", padLines(inner, m.cardW())), m.contentHeight)
}

// shortAppDigest keeps a row honest without overflowing it.
func shortAppDigest(d string) string {
	if len(d) > len("sha256:")+7 {
		return d[:len("sha256:")+7] + "…"
	}
	return d
}

// firstAppWord trims a health note to its leading word for row use.
func firstAppWord(s string) string {
	if i := strings.IndexAny(s, " "); i > 0 {
		return s[:i]
	}
	return s
}

// deployView renders the deploy workflow entry point: deploys run
// from the project directory, where discovery reads mymo.yaml — the
// TUI points the way; the command holds the hands.
func (m Model) deployView() string {
	inner := titleStyle.Render("Deploy") + "\n\n" +
		"Every deployment follows the same short path:\n\n" +
		"  1. Discover    read mymo.yaml — or draft one for a Dockerfile\n" +
		"  2. Target      a node mymo has verified READY\n" +
		"  3. Confirm    the app's name, typed in full\n" +
		"  4. Execute    build or pull, health-gate, activate\n" +
		"  5. Keep       the previous release, ready to roll back\n\n" +
		"Deploys run from the project directory:\n" +
		accentStyle.Render("  mymo deploy -node <name>") + "\n\n" +
		faintStyle.Render("Ports are never published: apps live on mymo's network,") + "\n" +
		faintStyle.Render("Caddy alone faces the internet.")
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
