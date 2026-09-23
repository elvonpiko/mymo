package tui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
)

// fleetItem adapts a domain node for the fleet list.
type fleetItem struct{ node domain.Node }

// FilterValue makes fleet items filterable by node name.
func (i fleetItem) FilterValue() string { return i.node.Name }

// fleetDelegate renders fleet rows: cursor, dot, name, address, mode badge.
type fleetDelegate struct{}

func (fleetDelegate) Height() int                             { return 1 }
func (fleetDelegate) Spacing() int                            { return 0 }
func (fleetDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (fleetDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(fleetItem)
	if !ok {
		return
	}
	cursor := "  "
	nameStyle := itemStyle
	if m.Index() == index {
		cursor = "> "
		nameStyle = itemSelStyle
	}
	addr := fmt.Sprintf("%s@%s", it.node.User, it.node.Address())
	line := cursor +
		faintStyle.Render("● ") +
		nameStyle.Width(20).Render(it.node.Name) +
		dimStyle.Width(30).Render(addr) +
		modeBadge(it.node.Mode) +
		"  " + faintStyle.Render("unverified")
	fmt.Fprint(w, line)
}

// modeBadge renders the node mode column.
func modeBadge(mode domain.NodeMode) string {
	switch mode {
	case domain.ModeAppHost:
		return accentStyle.Render("[app-host]")
	default:
		return faintStyle.Render("[observe]")
	}
}

// fleetItems converts nodes to list items.
func fleetItems(nodes []domain.Node) []list.Item {
	items := make([]list.Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, fleetItem{node: n})
	}
	return items
}

// newFleetList builds the fleet list model for the given nodes.
func newFleetList(nodes []domain.Node) list.Model {
	l := list.New(fleetItems(nodes), fleetDelegate{}, 80, 20)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	return l
}

// fleetView renders the fleet screen: the NODES list over an
// APPLICATIONS summary, filling the content area.
func (m Model) fleetView() string {
	if m.loadErr != nil {
		return m.loadErrView()
	}
	if len(m.nodes) == 0 {
		return m.emptyFleetView()
	}
	var b strings.Builder
	b.WriteString(sectionLabel("NODES"))
	b.WriteString("\n")
	b.WriteString(m.fleet.View())
	b.WriteString("\n\n")
	b.WriteString(sectionLabel("APPLICATIONS"))
	b.WriteString("\n")
	b.WriteString(faintStyle.Render("none managed yet · press d for the deploy workflow"))
	return fitHeight(b.String(), m.contentHeight)
}

// loadErrView renders a state-loading failure.
func (m Model) loadErrView() string {
	inner := errStyle.Render("mymo could not read its local state") + "\n\n" +
		textStyle.Render(m.loadErr.Error()) + "\n\n" +
		faintStyle.Render("state lives in ~/.mymo as plain JSON; fix the file and relaunch")
	panel := panelStyle.Render(inner)
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panel)
}

// emptyFleetView renders the node list with nothing in it: what mymo
// is, the teaching card, and every key that leads somewhere.
func (m Model) emptyFleetView() string {
	inner := titleStyle.Render("No servers yet") + "\n\n" +
		"mymo manages your fleet from a single window:\n" +
		"nodes, applications, deployments, health.\n\n" +
		accentStyle.Render("[N]") + textStyle.Render(" add your first VPS") + "\n\n" +
		faintStyle.Render("mymo never modifies a server without your approval")
	block := m.revealedDesc() + "\n\n" +
		panelStyle.Render(inner) + "\n\n" +
		homeShortcutLine()
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, block)
}

// sectionLabel renders a section heading like "NODES".
func sectionLabel(s string) string {
	return sectionLabelStyle.Render(s)
}
