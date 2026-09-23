package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
)

// actionID identifies a node-screen action.
type actionID int

const (
	actApps actionID = iota
	actInspect
	actSSH
	actRemove
)

// actionDefs lists the node actions in display order.
func actionDefs() []actionItem {
	return []actionItem{
		{actApps, "Applications", "list applications on this node"},
		{actInspect, "Inspect record", "the full stored state for this node"},
		{actSSH, "SSH", "open an interactive session"},
		{actRemove, "Remove", "remove this node from mymo"},
	}
}

// actionItem adapts a node action for the actions list.
type actionItem struct {
	id          actionID
	title, desc string
}

// FilterValue makes actions filterable by title.
func (a actionItem) FilterValue() string { return a.title }

// actionDelegate renders action rows: title plus a muted description.
type actionDelegate struct{}

func (actionDelegate) Height() int                             { return 1 }
func (actionDelegate) Spacing() int                            { return 0 }
func (actionDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (actionDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	a, ok := item.(actionItem)
	if !ok {
		return
	}
	cursor := "  "
	titleStyle := itemStyle
	if m.Index() == index {
		cursor = "> "
		titleStyle = itemSelStyle
	}
	fmt.Fprint(w, cursor+titleStyle.Width(18).Render(a.title)+dimStyle.Render(a.desc))
}

// newActionsList builds the node actions list.
func newActionsList() list.Model {
	items := make([]list.Item, 0, len(actionDefs()))
	for _, a := range actionDefs() {
		items = append(items, a)
	}
	l := list.New(items, actionDelegate{}, 80, len(actionDefs()))
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	return l
}

// nodeView renders the node overview: status, facts, and actions.
func (m Model) nodeView() string {
	n := m.selNode
	var b strings.Builder
	b.WriteString(faintStyle.Render("● ") + titleStyle.Render(n.Name) + " " + modeBadge(n.Mode))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("%s@%s · auth %s · added %s",
		n.User, n.Address(), n.Auth, n.AddedAt.Format("2006-01-02"))))
	b.WriteString("\n\n")
	b.WriteString(factsPanel(n, "host", "port", "user", "auth", "mode"))
	b.WriteString("\n\n")
	b.WriteString(sectionLabel("ACTIONS"))
	b.WriteString("\n")
	b.WriteString(m.actions.View())
	return fitHeight(b.String(), m.contentHeight)
}

// factsPanel renders a bordered key/value panel for selected node fields.
// Empty values are omitted.
func factsPanel(n domain.Node, fields ...string) string {
	var rows [][2]string
	for _, f := range fields {
		switch f {
		case "name":
			rows = append(rows, [2]string{"name", n.Name})
		case "host":
			rows = append(rows, [2]string{"host", n.Host})
		case "port":
			rows = append(rows, [2]string{"port", strconv.Itoa(n.Port)})
		case "user":
			rows = append(rows, [2]string{"user", n.User})
		case "auth":
			rows = append(rows, [2]string{"auth", string(n.Auth)})
		case "key path":
			if n.KeyPath != "" {
				rows = append(rows, [2]string{"key path", n.KeyPath})
			}
		case "mode":
			rows = append(rows, [2]string{"mode", string(n.Mode)})
		case "added":
			rows = append(rows, [2]string{"added", n.AddedAt.Format("2006-01-02 15:04")})
		}
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(factsLabelStyle.Width(12).Render(r[0]))
		b.WriteString(" ")
		b.WriteString(textStyle.Render(r[1]))
		b.WriteString("\n")
	}
	return factsPanelStyle.Render(strings.TrimRight(b.String(), "\n"))
}

// inspectView renders the node's full stored record.
func (m Model) inspectView() string {
	n := m.selNode
	inner := titleStyle.Render("Stored record") + "\n\n" +
		factsPanel(n, "name", "host", "port", "user", "auth", "key path", "mode", "added") +
		"\n\n" + faintStyle.Render("the stored record is local state, not live facts;") + "\n" +
		faintStyle.Render("live discovery arrives with the SSH transport")
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, inner)
}

// sshView renders the SSH entry point with the exact manual command.
func (m Model) sshView() string {
	n := m.selNode
	cmd := fmt.Sprintf("ssh -p %d %s@%s", n.Port, n.User, n.Host)
	if n.Auth == domain.AuthKey && n.KeyPath != "" {
		cmd += " -i " + n.KeyPath
	}
	inner := titleStyle.Render("SSH into "+n.Name) + "\n\n" +
		"Interactive sessions open through mymo's SSH transport,\n" +
		"which arrives with the next phase.\n\n" +
		"For now, connect directly:\n\n" +
		codeStyle.Render(cmd) + "\n\n" +
		faintStyle.Render("mymo records the connection; your plain ssh stays available")
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panelStyle.Render(inner))
}

// removeConfirmView asks for explicit confirmation before removing a node.
func (m Model) removeConfirmView() string {
	inner := warnStyle.Render("Remove "+m.selNode.Name+" from the fleet?") + "\n\n" +
		"The server itself is not touched — only mymo's\n" +
		"local record is removed.\n\n" +
		accentStyle.Render("[y]") + textStyle.Render(" remove    ") +
		accentStyle.Render("[esc]") + textStyle.Render(" cancel")
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, dangerCardStyle.Render(inner))
}
