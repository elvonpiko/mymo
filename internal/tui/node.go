package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
)

// actionID identifies an observe-page action.
type actionID int

const (
	actSSH actionID = iota
	actApps
	actInspect
	actRemove
)

// actionDefs lists the observe page's actions. Checking is not among
// them: entering the page is the check.
func actionDefs() []actionItem {
	return []actionItem{
		{actSSH, "SSH", "open an interactive session on the node"},
		{actApps, "Applications", "list applications on this node"},
		{actInspect, "Inspect record", "the full stored state for this node"},
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

// nodeView renders the observe page: health heading, the SYSTEM and
// LIVE cards, and the node's actions.
func (m Model) nodeView() string {
	n := m.selNode
	var b strings.Builder
	b.WriteString(nodeStatusLine(n, modeBadge(n.Mode)))
	b.WriteString("\n")
	auth := fmt.Sprintf("%s auth", n.Auth)
	if n.Auth == domain.AuthKey {
		auth = "key auth"
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("%s@%s · %s · added %s",
		n.User, n.Address(), auth, n.AddedAt.Format("2006-01-02"))))
	b.WriteString("\n")
	b.WriteString(m.systemCard(n))
	// the LIVE card exists only once something is there to sample,
	// and spans the same width as the SYSTEM card beside it
	if !n.Facts.CollectedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(titledCard("LIVE", padLines(m.liveCardInner(), cardGridW)))
	}
	b.WriteString("\n")
	b.WriteString(sectionLabel("ACTIONS"))
	b.WriteString("\n")
	b.WriteString(m.actions.View())
	return fitHeight(b.String(), m.contentHeight)
}

// loadingView is the loading ceremony, shared by any wait mymo owes
// the user a face for: mymo's mark and word, a spinner saying what is
// running, and for how long — updated live by the spinner's ticks.
func (m Model) loadingView() string {
	elapsed := time.Since(m.loading.start).Truncate(time.Second)
	inner := brandIcon() + " " + brandStyle.Render("mymo") + "\n\n" +
		m.spinner.View() + " " + subtextStyle.Render(m.loading.line) + "\n" +
		faintStyle.Render(m.loading.detail) + "\n" +
		faintStyle.Render(elapsed.String())
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, inner)
}

// systemCard renders the captured snapshot as the SYSTEM panel: five
// two-column rows of the node's identity in glance form. An
// unchecked node is told how observation works; a failed check keeps
// the last good facts and names the failure.
func (m Model) systemCard(n domain.Node) string {
	var inner string
	switch {
	case n.Facts.CollectedAt.IsZero() && n.LastCheck.At.IsZero():
		inner = faintStyle.Render("no snapshot yet") + "\n" +
			textStyle.Render("entering a node observes it — leave and") + "\n" +
			textStyle.Render("re-enter to try again")
	case n.Facts.CollectedAt.IsZero():
		// checked before, never successfully
		inner = errStyle.Render("first check failed: ") + subtextStyle.Render(shorten(n.LastCheck.Error, 44)) + "\n" +
			faintStyle.Render("esc, then re-enter to retry")
	default:
		rows := systemRows(n)
		var b strings.Builder
		for i := 0; i < len(rows); i += 2 {
			left := discoveredCell(rows[i])
			right := ""
			if i+1 < len(rows) {
				right = discoveredCell(rows[i+1])
			}
			b.WriteString(left + right + "\n")
		}
		inner = strings.TrimRight(b.String(), "\n")
		if n.LastCheck.Error != "" {
			inner += "\n" + errStyle.Render("last check failed: ") + subtextStyle.Render(shorten(n.LastCheck.Error, 44))
		}
	}
	return titledCard("SYSTEM", inner)
}

// systemRows builds the SYSTEM card's label/value pairs in display
// order — the snapshot's identity, in glance form.
func systemRows(n domain.Node) [][2]string {
	f := n.Facts
	uptime := "unknown"
	if f.Uptime > 0 {
		uptime = facts.FormatUptimeShort(f.Uptime)
	}
	cpus := "unknown"
	if f.CPUs > 0 {
		cpus = strconv.Itoa(f.CPUs)
	}
	systemd := faintStyle.Render("—")
	if f.Systemd {
		systemd = okStyle.Render("✓")
	}
	return [][2]string{
		{"OS", orUnknown(f.OS)},
		{"Kernel", orUnknown(f.Kernel)},
		{"Arch", orUnknown(f.Arch)},
		{"CPUs", cpus},
		{"Uptime", uptime},
		{"User", orUnknown(f.User)},
		{"Disk", diskText(f)},
		{"Systemd", systemd},
		{"Docker", toolShort(facts.DockerVersion(f.Docker))},
		{"Caddy", toolShort(f.Caddy)},
	}
}

// toolShort renders a tool's version, or a dash when it is absent —
// the record keeps the words, the card keeps the glance.
func toolShort(version string) string {
	if version == "" {
		return faintStyle.Render("—")
	}
	return version
}

// titledCard renders a panel whose border carries its name: SYSTEM,
// LIVE. The title sits in the top border like a chapter heading.
func titledCard(title, inner string) string {
	card := factsPanelStyle.Render(inner)
	lines := strings.Split(card, "\n")
	w := lipgloss.Width(lines[0]) - 4 // inner width, padding included
	dashes := w - 3 - lipgloss.Width(title) - 1
	if dashes < 1 {
		return card
	}
	lines[0] = cardBorderStyle.Render("╭─ ") + cardTitleStyle.Render(title) + " " +
		cardBorderStyle.Render(strings.Repeat("─", dashes)+"╮")
	return strings.Join(lines, "\n")
}

// discoveredRows builds the snapshot's label/value pairs, one pair per
// cell, in display order. Zero facts stay "unknown" — never a guess.
func (m Model) discoveredRows(n domain.Node) [][2]string {
	f := n.Facts
	uptime := "unknown"
	if f.Uptime > 0 {
		uptime = facts.FormatUptime(f.Uptime)
	}
	cpus := "unknown"
	if f.CPUs > 0 {
		cpus = strconv.Itoa(f.CPUs)
	}
	return [][2]string{
		{"os", orUnknown(f.OS)},
		{"arch", orUnknown(f.Arch)},
		{"kernel", orUnknown(f.Kernel)},
		{"cpus", cpus},
		{"uptime", uptime},
		{"user", orUnknown(f.User)},
		{"memory", memText(f)},
		{"disk", diskText(f)},
		{"docker", toolText(facts.DockerVersion(f.Docker))},
		{"caddy", toolText(f.Caddy)},
		{"systemd", yesNo(f.Systemd)},
		{"checked", orUnknown(facts.FormatAge(n.LastCheck.At))},
	}
}

// cardGridW is the facts grid's inner width: two 37-wide cells. The
// LIVE card pads to it so both cards share one silhouette.
const cardGridW = 74

// padLines spaces every line of s out to width w, ANSI-aware, so
// differently sized contents render as one uniform card.
func padLines(s string, w int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		gap := w - lipgloss.Width(line)
		if gap < 0 {
			gap = 0
		}
		out = append(out, line+strings.Repeat(" ", gap))
	}
	return strings.Join(out, "\n")
}

// discoveredCell renders one label/value cell, fixed width so the
// two-column grid aligns; long values truncate with an ellipsis.
func discoveredCell(row [2]string) string {
	return factsLabelStyle.Width(9).Render(row[0]) + " " +
		textStyle.Width(27).Render(shorten(row[1], 27))
}

// memText renders "3.2/3.8 GiB avail" — headroom first, then capacity.
func memText(f facts.Node) string {
	if f.MemTotal == 0 {
		return "unknown"
	}
	total := facts.FormatBytes(f.MemTotal)
	if f.MemAvail == 0 {
		return total
	}
	return facts.FormatBytes(f.MemAvail) + "/" + total + " avail"
}

// diskText renders "32/39 GiB free" for the root filesystem.
func diskText(f facts.Node) string {
	if f.DiskTotal == 0 {
		return "unknown"
	}
	total := facts.FormatBytes(f.DiskTotal)
	if f.DiskFree == 0 {
		return total
	}
	return facts.FormatBytes(f.DiskFree) + "/" + total + " free"
}

func toolText(version string) string {
	if version == "" {
		return "not installed"
	}
	return version
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// shorten trims s to at most n visible characters for one-line
// display.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
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
		case "last check":
			if n.LastCheck.At.IsZero() {
				rows = append(rows, [2]string{"last check", "never"})
			} else if n.LastCheck.Error != "" {
				rows = append(rows, [2]string{"last check", "failed " + facts.FormatAge(n.LastCheck.At)})
			} else {
				rows = append(rows, [2]string{"last check", "ok " + facts.FormatAge(n.LastCheck.At)})
			}
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

// inspectView renders the node's full stored record beside its
// discovered snapshot — everything mymo knows in one place.
func (m Model) inspectView() string {
	n := m.selNode
	record := titleStyle.Render("Stored record") + "\n\n" +
		factsPanel(n, "name", "host", "port", "user", "auth", "key path", "mode", "added", "last check")
	discovered := titleStyle.Render("Discovered") + "\n\n"
	if n.Facts.CollectedAt.IsZero() && n.LastCheck.At.IsZero() {
		discovered += factsPanelStyle.Render("not probed yet")
	} else {
		var b strings.Builder
		for _, r := range m.discoveredRows(n) {
			b.WriteString(factsLabelStyle.Width(12).Render(r[0]))
			b.WriteString(" ")
			b.WriteString(textStyle.Render(shorten(r[1], 30)))
			b.WriteString("\n")
		}
		discovered += factsPanelStyle.Render(strings.TrimRight(b.String(), "\n"))
		if n.LastCheck.Error != "" {
			discovered += "\n" + errStyle.Render("last check failed: ") +
				subtextStyle.Render(shorten(n.LastCheck.Error, 38))
		}
	}
	inner := lipgloss.JoinHorizontal(lipgloss.Top, record, "  ", discovered)
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, inner)
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
