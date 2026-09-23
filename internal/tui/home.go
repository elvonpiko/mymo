package tui

import (
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// descLines is mymo's self-description, revealed letter by letter on the
// home page and on the empty fleet page — what mymo is, the opinionated
// way of running a fleet.
var descLines = []string{
	"an opinionated window onto your fleet:",
	"nodes, applications, deployments, health —",
	"local-first, from one terminal.",
}

// descRunes is the full description as one rune stream, typed across the
// line breaks like a single sentence.
func descRunes() []rune {
	return []rune(strings.Join(descLines, "\n"))
}

// descTickMsg advances the typewriter reveal of the description.
type descTickMsg struct{}

// animDesc schedules the next typewriter tick.
func animDesc() tea.Cmd {
	return tea.Tick(28*time.Millisecond, func(time.Time) tea.Msg {
		return descTickMsg{}
	})
}

// beginDesc restarts the typewriter from its first letter.
func (m *Model) beginDesc() tea.Cmd {
	m.descShown = 0
	m.descAnim = true
	return animDesc()
}

// advanceDesc reveals one more letter while a page that shows the
// description is on screen; navigating elsewhere ends the animation.
func (m *Model) advanceDesc() tea.Cmd {
	if m.descShown >= len(descRunes()) {
		m.descAnim = false
		return nil
	}
	m.descShown++
	if m.descShown >= len(descRunes()) {
		m.descAnim = false
		return nil
	}
	return animDesc()
}

// revealedDesc renders the description up to the letters revealed so far.
func (m Model) revealedDesc() string {
	r := descRunes()
	if m.descShown > len(r) {
		m.descShown = len(r)
	}
	return subtextStyle.Render(string(r[:m.descShown]))
}

// shortcutEntry renders one "key label" pair of a shortcut guide.
func shortcutEntry(k, label string) string {
	return accentStyle.Render(k) + "  " + subtextStyle.Render(label)
}

// homeShortcutGrid is the home page's two-column key guide.
func homeShortcutGrid() string {
	left := strings.Join([]string{
		shortcutEntry("n", "nodes"),
		shortcutEntry("a", "applications"),
		shortcutEntry("s", "settings"),
	}, "\n")
	right := strings.Join([]string{
		shortcutEntry("N", "add node"),
		shortcutEntry("d", "deploy"),
		shortcutEntry("?", "help"),
	}, "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right)
}

// homeShortcutLine is a compact one-row key guide for the empty fleet
// page.
func homeShortcutLine() string {
	parts := []string{
		shortcutEntry("N", "add node"),
		shortcutEntry("a", "apps"),
		shortcutEntry("d", "deploy"),
		shortcutEntry("s", "settings"),
		shortcutEntry("esc", "home"),
		shortcutEntry("?", "help"),
	}
	return strings.Join(parts, faintStyle.Render(" · "))
}

// homeView renders the home hub: the mark, the brand, the description
// typing itself out, and where every key leads.
func (m Model) homeView() string {
	if m.loadErr != nil {
		return m.loadErrView()
	}
	lines := []string{
		brandIcon(),
		"",
		brandStyle.Render("mymo"),
		"",
		m.revealedDesc(),
		"",
		homeShortcutGrid(),
	}
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}
