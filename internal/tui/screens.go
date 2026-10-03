package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
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
	return fitHeight(titledCard("Applications", padLines(inner, m.cardW())), m.contentHeight)
}

// appDetailView renders one application's truth: what it is, how it
// is exposed, and every release it has had — the active one live,
// the retired one kept, the failed one honest about why.
func (m Model) appDetailView(s screen) string {
	var app *domain.App
	for i := range m.apps {
		if m.apps[i].Name == s.app && m.apps[i].Node == s.node {
			app = &m.apps[i]
		}
	}
	if app == nil {
		return m.infoView("Application",
			[]string{s.app + " is not in the record — it may have been removed."},
			"[esc] back")
	}

	artifact := app.Image
	if app.Type == domain.SourceDockerfile {
		artifact = "Dockerfile in " + app.Dockerfile
	}
	expose := "internal only · " + fmt.Sprint(app.Port)
	exposeStyle := subtextStyle
	if app.Domain != "" {
		expose = "https://" + app.Domain + " · port " + fmt.Sprint(app.Port)
		exposeStyle = textStyle
	}
	health := "not declared — a running container is all mymo can vouch for"
	if app.Health != "" {
		health = app.Health
	}

	inner := titleStyle.Render(app.Name) + faintStyle.Render(" · "+app.Node) + "\n\n" +
		factsLabelStyle.Width(10).Render("type") + textStyle.Render(string(app.Type)+" · "+artifact) + "\n" +
		factsLabelStyle.Width(10).Render("exposure") + exposeStyle.Render(expose) + "\n" +
		factsLabelStyle.Width(10).Render("health") + subtextStyle.Render(shorten(health, 58)) + "\n\n"

	if len(app.Releases) == 0 {
		inner += faintStyle.Render("no releases yet — nothing has been deployed")
	} else {
		inner += factsLabelStyle.Width(10).Render("releases") + "\n"
		for _, r := range app.Releases {
			state, stateStyle := "active", okStyle
			switch r.State {
			case domain.ReleaseRetired:
				state, stateStyle = "kept", subtextStyle
			case domain.ReleaseFailed:
				state, stateStyle = "failed", errStyle
			}
			digest := shortAppDigest(r.Digest)
			age := "unknown when"
			if !r.DeployedAt.IsZero() {
				age = facts.FormatAge(r.DeployedAt)
			}
			line := stateStyle.Width(7).Render(state) + dimStyle.Width(17).Render(digest)
			if r.Health != "" {
				if r.State == domain.ReleaseFailed {
					line += subtextStyle.Render(shorten(r.Health, 34))
				} else {
					line += subtextStyle.Render(shorten(r.Health, 14))
				}
			}
			line += " " + faintStyle.Render(age)
			inner += factsLabelStyle.Width(10).Render("r"+fmt.Sprint(r.ID)) + line + "\n"
		}
	}
	inner += "\n" + faintStyle.Render("operate with: mymo app status|logs|restart|rollback "+app.Name)
	// a work page rides the stage like the apply report: padded to
	// the stage width so the block centers with uniform gutters
	return fitHeight(padLines(inner, m.stageW), m.contentHeight)
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
