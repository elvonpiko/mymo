package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The unreachable page: a box that did not answer gets its own
// state, in the same chromeless family as the loading page — the
// red mark says it before any word does. It never renders the last
// known dashboard as if it were live: the record is one key away
// (i), and the honest sentence is the page.

// unreachClass turns the stored failure into plain words: what
// happened, in the operator's vocabulary, and what it usually means.
// The classes ride the ssh package's sentinel prefixes, which every
// wrap keeps as its head.
func unreachClass(err string) (title, note string) {
	switch {
	case strings.HasPrefix(err, "unreachable"):
		return "did not answer",
			"mymo dialed and the box never picked up — it may be off, its network may be down, or its address changed"
	case strings.HasPrefix(err, "host key mismatch"):
		return "is not the box mymo met",
			"the host key changed — a rebuild does this, and so does a man-in-the-middle; mymo never guesses which"
	case strings.HasPrefix(err, "authentication failed"):
		return "refused mymo's login",
			"the box did not accept the key — the user or the key may have changed on the box"
	case strings.HasPrefix(err, "ssh agent unavailable"):
		return "refused mymo's login",
			"the ssh agent is unavailable or holds no usable keys"
	default:
		return "could not be checked", err
	}
}

// errorCeremonyView is mymo's error page — the one design every
// failure renders through, the loading ceremony's mirror: the mark
// in alarm, the words centered, no header, no chrome. The headline
// names what failed; the note explains in plain words; the raw
// reason rides faint below; the last word is the reassurance that
// belongs to this failure. The moves live in the footer, where
// every page's keys live — the body states the failure, the
// footer states what can be done about it.
func (m Model) errorCeremonyView(headline, note, raw, lastWord string) string {
	inner := brandIconErr() + " " + errStyle.Render("mymo") + "\n\n" +
		headline + "\n\n"

	for _, line := range wrapPlain(note, 62) {
		inner += textStyle.Render(line) + "\n"
	}
	inner += "\n"

	for _, line := range wrapDetail(raw, 62) {
		inner += faintStyle.Render(line) + "\n"
	}
	if lastWord != "" {
		inner += "\n" + faintStyle.Render(lastWord) + "\n"
	}

	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, inner)
}

// unreachableView renders the box that did not answer through the
// error ceremony: name and what happened, the raw reason, the
// reassurance that nothing ran, the last-seen fact.
func (m Model) unreachableView() string {
	n := m.selNode
	title, note := unreachClass(m.nodeCheckErr)

	headline := titleStyle.Render(n.Name) + errStyle.Render(" "+title)
	lastWord := "nothing ran — the box was not touched"
	if !n.Facts.CollectedAt.IsZero() {
		lastWord = "nothing ran · mymo last saw it " + ago(n.Facts.CollectedAt)
	}

	return m.errorCeremonyView(headline, note, m.nodeCheckErr, lastWord)
}

// ago renders a duration since t in the words the page uses.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "moments ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// updateUnreachKeys: while the retry ceremony runs, its loading page
// owns the keys; afterwards r re-probes, x opens the removal gate,
// i shows the record.
func (m Model) updateUnreachKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.loading.active {
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc":
			m.cancelLoading()
			return m, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "r", "enter":
		return m, m.retryNodeCheck()
	case "i":
		m.push(screen{kind: scNodeInspect, node: m.selNode.Name})
		return m, nil
	case "x":
		m.confirmRemove = true
		return m, nil
	}
	return m, nil
}

// retryNodeCheck re-runs the entry probe without leaving the page:
// the same ceremony as entering — one dial, read-only, narrated.
func (m *Model) retryNodeCheck() tea.Cmd {
	n := m.selNode
	m.nodeCheckErr = ""
	m.loading = loadingState{
		active: true,
		line:   "observing " + n.Name + " over SSH",
		detail: "ten read-only commands · nothing is modified",
		node:   n.Name,
		start:  time.Now(),
	}
	m.checkSeq++
	m.fleetSetChecking(n.Name)
	m.layout()
	return tea.Batch(m.spinner.Tick, m.beginCheck(n, m.checkSeq))
}

// unreachKeymap: the footer mirrors the moves the page offers.
type unreachKeymap struct {
	retry   key.Binding
	remove  key.Binding
	inspect key.Binding
	back    key.Binding
	help    key.Binding
	quit    key.Binding
}

func newUnreachKeymap() unreachKeymap {
	return unreachKeymap{
		retry:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "retry")),
		remove:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
		inspect: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inspect")),
		back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k unreachKeymap) ShortHelp() []key.Binding {
	// the moves live in the footer, where every page's keys live;
	// the body states the failure, not the buttons
	return []key.Binding{k.retry, k.remove, k.inspect, k.back, k.help, k.quit}
}

func (k unreachKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.retry, k.remove, k.inspect, k.back}, {k.help, k.quit}}
}
