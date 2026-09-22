package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
)

// fleetItem adapts a domain node for the fleet list.
type fleetItem struct {
	node domain.Node
}

// FilterValue makes fleet items filterable by node name.
func (i fleetItem) FilterValue() string { return i.node.Name }

// fleetDelegate renders fleet rows: cursor, name, user@address, mode badge.
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
		nameStyle.Width(24).Render(it.node.Name) +
		dimStyle.Width(32).Render(addr) +
		modeBadge(it.node.Mode)
	fmt.Fprint(w, line)
}

// modeBadge renders the node mode column.
func modeBadge(mode domain.NodeMode) string {
	switch mode {
	case domain.ModeAppHost:
		return appHostStyle.Render("[app-host]")
	default:
		return dimStyle.Render("[observe]")
	}
}

// newFleetList builds the fleet list model for the given nodes.
func newFleetList(nodes []domain.Node) list.Model {
	items := make([]list.Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, fleetItem{node: n})
	}
	l := list.New(items, fleetDelegate{}, 80, 20)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	return l
}

// fleetView renders the fleet screen.
func (m Model) fleetView() string {
	var b strings.Builder
	b.WriteString(m.header("fleet", strconv.Itoa(len(m.nodes))+" nodes"))
	b.WriteString("\n\n")
	switch {
	case m.loadErr != nil:
		b.WriteString(errStyle.Render("failed to load state: " + m.loadErr.Error()))
	case len(m.nodes) == 0:
		b.WriteString(emptyHint())
	default:
		b.WriteString(m.fleet.View())
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(m.help.View(fleetKeys)))
	return b.String()
}

// nodeView renders the node detail screen.
func (m Model) nodeView() string {
	var b strings.Builder
	b.WriteString(m.header("node "+m.sel.Name, string(m.sel.Mode)))
	b.WriteString("\n\n")
	b.WriteString(nodeDetail(m.sel))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(m.help.View(nodeKeys)))
	return b.String()
}

// header renders the shared top line.
func (m Model) header(title, right string) string {
	h := appStyle.Render("mymo") + " " + titleStyle.Render(title)
	if right != "" {
		h += " " + dimStyle.Render("· "+right)
	}
	return h
}

// emptyHint renders the empty-fleet hint box.
func emptyHint() string {
	lines := []string{
		"No nodes yet.",
		"",
		"Add your first node with:",
		"  mymo node add",
	}
	return borderStyle.Render(strings.Join(lines, "\n"))
}

// nodeDetail renders the key/value panel for a stored node record.
func nodeDetail(n domain.Node) string {
	rows := [][2]string{
		{"host", n.Host},
		{"port", strconv.Itoa(n.Port)},
		{"user", n.User},
		{"auth", string(n.Auth)},
	}
	if n.KeyPath != "" {
		rows = append(rows, [2]string{"key path", n.KeyPath})
	}
	rows = append(rows,
		[2]string{"mode", string(n.Mode)},
		[2]string{"added", n.AddedAt.Format(time.RFC3339)},
	)
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(labelStyle.Width(12).Render(r[0] + ":"))
		b.WriteString(" ")
		b.WriteString(r[1])
		b.WriteString("\n")
	}
	return borderStyle.Render(strings.TrimRight(b.String(), "\n"))
}
