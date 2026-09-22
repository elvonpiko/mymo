package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
	"github.com/elvonpiko/mymo/internal/version"
)

// Model is the mymo workspace root model. The UI is one persistent
// workspace with a navigation stack, not a sequence of pages.
type Model struct {
	store *state.Store
	nodes []domain.Node
	meta  state.MetaDoc

	stack    []screen
	fleet    list.Model
	actions  list.Model
	help     help.Model
	fullHelp help.Model

	width, height int
	contentWidth  int
	contentHeight int

	helpOpen      bool
	confirmRemove bool
	selNode       domain.Node
	loadErr       error
	toast         *toast

	addNode addNodeState

	introDone bool
	introStep int
	introErr  string
}

// New builds the workspace model from the given store. Loading failures are
// surfaced inside the UI, not here.
func New(store *state.Store) Model {
	m := Model{
		store:    store,
		stack:    []screen{{kind: scFleet}},
		help:     help.New(),
		fullHelp: help.New(),
		width:    80,
		height:   24,
	}
	m.fullHelp.ShowAll = true
	if store != nil {
		if nodes, err := store.LoadNodes(); err == nil {
			m.nodes = nodes
		} else {
			m.loadErr = err
		}
		meta, err := store.LoadMeta()
		if err != nil {
			m.loadErr = err
		} else {
			m.meta = meta
			m.introDone = meta.IntroSeen
		}
	}
	m.fleet = newFleetList(m.nodes)
	m.actions = newActionsList()
	m.layout()
	return m
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	if !m.introDone {
		return m.nextIntroTick()
	}
	return nil
}

// Update satisfies tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While the embedded add-node form is active it owns every message:
	// huh advances its fields through its own internal commands, and the
	// messages those produce must be routed back to the form — not just
	// key presses.
	if m.formActive() {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width, m.height = msg.Width, msg.Height
			m.layout()
			return m, nil

		case toastExpiredMsg:
			if m.toast != nil && m.toast.id == msg.id {
				m.toast = nil
			}
			return m, nil

		case introTickMsg:
			return m, nil
		}
		return m.updateAddForm(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case toastExpiredMsg:
		if m.toast != nil && m.toast.id == msg.id {
			m.toast = nil
		}
		return m, nil

	case introTickMsg:
		if !m.introDone && m.introStep < introSteps {
			m.introStep++
			return m, m.nextIntroTick()
		}
		return m, nil

	case tea.KeyPressMsg:
		if !m.introDone {
			return m.updateIntro(msg)
		}
		return m.updateKeys(msg)
	}
	// Non-key messages (mouse, internal) go to the active screen's widget.
	return m.forward(msg)
}

// formActive reports whether the embedded add-node form is receiving input.
func (m Model) formActive() bool {
	return m.cur().kind == scAddNode && m.addNode.stage == anForm && m.addNode.form != nil
}

// updateKeys routes workspace key presses.
func (m Model) updateKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.cur()

	// Keys typed into the fleet filter belong to the filter.
	if m.filterActive() && msg.String() != "ctrl+c" {
		return m.forward(msg)
	}

	str := msg.String()

	if str == "?" {
		m.helpOpen = !m.helpOpen
		return m, nil
	}
	if m.helpOpen {
		m.helpOpen = false
		return m, nil
	}
	if str == "ctrl+c" {
		return m, tea.Quit
	}

	switch s.kind {
	case scFleet:
		return m.updateFleetKeys(msg)
	case scNode:
		if m.confirmRemove {
			return m.updateRemoveConfirm(str)
		}
		return m.updateNodeKeys(msg)
	case scAddNode:
		return m.updateAddReview(str)
	default:
		return m.updateSimpleKeys(s.kind, str)
	}
}

// updateFleetKeys handles keys on the fleet screen.
func (m Model) updateFleetKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "n":
		return m.startAddNode()
	case "a":
		m.push(screen{kind: scApps})
		return m, nil
	case "d":
		m.push(screen{kind: scDeploy})
		return m, nil
	case "s":
		m.push(screen{kind: scSettings})
		return m, nil
	case "enter":
		if item, ok := m.fleet.SelectedItem().(fleetItem); ok {
			m.openNode(item.node)
		}
		return m, nil
	}
	return m.forward(msg)
}

// updateNodeKeys handles keys on the node overview screen.
func (m Model) updateNodeKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "enter":
		if a, ok := m.actions.SelectedItem().(actionItem); ok {
			return m.runAction(a)
		}
		return m, nil
	}
	return m.forward(msg)
}

// updateSimpleKeys handles keys on informational screens.
func (m Model) updateSimpleKeys(kind screenKind, str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "i":
		if kind == scSettings {
			m.introDone = false
			m.introStep = 0
			return m, m.nextIntroTick()
		}
	}
	return m, nil
}

// updateRemoveConfirm handles keys on the remove-node confirmation.
func (m Model) updateRemoveConfirm(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "y", "Y", "enter":
		if m.store == nil {
			m.confirmRemove = false
			return m, m.notify("state store unavailable", toastErr)
		}
		if err := m.store.DeleteNode(m.selNode.Name); err != nil {
			m.confirmRemove = false
			return m, m.notify(err.Error(), toastErr)
		}
		name := m.selNode.Name
		m.confirmRemove = false
		m.pop()
		m.reloadFleet()
		return m, m.notify("removed "+name+" from the fleet", toastOK)
	case "esc", "n", "N":
		m.confirmRemove = false
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

// runAction executes a node-screen action.
func (m Model) runAction(a actionItem) (tea.Model, tea.Cmd) {
	switch a.id {
	case actApps:
		m.push(screen{kind: scNodeApps, node: m.selNode.Name})
	case actInspect:
		m.push(screen{kind: scNodeInspect, node: m.selNode.Name})
	case actSSH:
		m.push(screen{kind: scNodeSSH, node: m.selNode.Name})
	case actRemove:
		m.confirmRemove = true
	}
	return m, nil
}

// openNode focuses a node: pushes its screen and rebuilds the action list.
func (m *Model) openNode(n domain.Node) {
	m.selNode = n
	m.actions = newActionsList()
	m.push(screen{kind: scNode, node: n.Name})
}

// filterActive reports whether the fleet filter is receiving keystrokes.
func (m Model) filterActive() bool {
	return m.cur().kind == scFleet && m.fleet.FilterState() == list.Filtering
}

// forward passes a message to the active screen's interactive widget.
func (m Model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.cur().kind {
	case scFleet:
		m.fleet, cmd = m.fleet.Update(msg)
	case scNode:
		m.actions, cmd = m.actions.Update(msg)
	}
	return m, cmd
}

// reloadFleet re-reads nodes from the store and rebuilds the fleet list.
func (m *Model) reloadFleet() {
	if m.store == nil {
		return
	}
	nodes, err := m.store.LoadNodes()
	if err != nil {
		m.loadErr = err
		return
	}
	m.loadErr = nil
	m.nodes = nodes
	m.fleet.SetItems(fleetItems(nodes))
}

// layout recomputes the frame and sizes all widgets to the window.
// Frame layout: header + rule + content + help + status.
func (m *Model) layout() {
	m.contentWidth = m.width
	m.contentHeight = m.height - 4
	if m.contentHeight < 1 {
		m.contentHeight = 1
	}
	fleetH := m.contentHeight - 4
	if fleetH < 1 {
		fleetH = 1
	}
	m.fleet.SetSize(m.contentWidth, fleetH)
	m.actions.SetSize(m.contentWidth, len(actionDefs()))
	if m.addNode.form != nil {
		w := m.contentWidth - 2
		if w > 72 {
			w = 72
		}
		if w < 30 {
			w = 30
		}
		m.addNode.form = m.addNode.form.WithWidth(w).WithHeight(m.contentHeight - 2)
	}
}

// View satisfies tea.Model.
func (m Model) View() tea.View {
	var content string
	if !m.introDone {
		content = m.introView()
	} else {
		content = m.workspaceView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// workspaceView renders the current screen inside the frame.
func (m Model) workspaceView() string {
	var content string
	if m.helpOpen {
		content = m.helpView()
	} else {
		switch s := m.cur(); s.kind {
		case scFleet:
			content = m.fleetView()
		case scNode:
			if m.confirmRemove {
				content = m.removeConfirmView()
			} else {
				content = m.nodeView()
			}
		case scNodeApps:
			content = m.nodeAppsView(s.node)
		case scNodeInspect:
			content = m.inspectView()
		case scNodeSSH:
			content = m.sshView()
		case scApps:
			content = m.allAppsView()
		case scDeploy:
			content = m.deployView()
		case scSettings:
			content = m.settingsView()
		case scAddNode:
			content = m.addView()
		}
	}
	return m.frame(content)
}

// frame wraps screen content with the shared header, rule, and footer,
// padding the content to an exact height so the frame always fills the
// terminal.
func (m Model) frame(content string) string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(faintStyle.Render(strings.Repeat("─", max(0, m.width))))
	b.WriteString("\n")
	b.WriteString(fitHeight(content, m.contentHeight))
	b.WriteString("\n")
	b.WriteString(m.footerView())
	return b.String()
}

// headerView renders brand, breadcrumbs, and the toast slot.
func (m Model) headerView() string {
	line := brandStyle.Render("mymo")
	for _, part := range m.crumbs() {
		line += faintStyle.Render(" / ") + subtextStyle.Render(part)
	}
	if m.toast != nil {
		t := m.toast.view()
		if gap := m.width - lipgloss.Width(line) - lipgloss.Width(t); gap > 0 {
			line += strings.Repeat(" ", gap) + t
		}
	}
	return line
}

// footerView renders the per-screen key help and the global status line.
func (m Model) footerView() string {
	helpLine := m.help.View(m.keymap())
	status := m.statusText()
	if gap := m.width - lipgloss.Width(helpLine) - lipgloss.Width(status); gap > 0 {
		return helpLine + strings.Repeat(" ", gap) + status + "\n" + m.versionLine()
	}
	return helpLine + "\n" + status + "  " + m.versionLine()
}

// statusText summarizes the fleet: node and application counts.
func (m Model) statusText() string {
	nodeWord := "nodes"
	if len(m.nodes) == 1 {
		nodeWord = "node"
	}
	return faintStyle.Render(fmt.Sprintf("%d %s · 0 applications", len(m.nodes), nodeWord))
}

// versionLine shows the running version, right-aligned on the status row.
func (m Model) versionLine() string {
	line := faintStyle.Render("mymo " + version.Version)
	if gap := m.width - lipgloss.Width(line); gap > 0 {
		return strings.Repeat(" ", gap) + line
	}
	return line
}

// keymap returns the help keymap for the current screen.
func (m Model) keymap() help.KeyMap {
	switch s := m.cur(); s.kind {
	case scFleet:
		return newFleetKeymap()
	case scNode:
		if m.confirmRemove {
			return newConfirmKeymap()
		}
		return newNodeKeymap()
	case scAddNode:
		return newAddReviewKeymap()
	default:
		return newSimpleKeymap()
	}
}

// helpView renders the expanded per-screen help overlay.
func (m Model) helpView() string {
	k := m.keymap()
	inner := titleStyle.Render("Keyboard help") + "\n\n" + m.fullHelp.View(k) +
		"\n\n" + faintStyle.Render("press any key to close")
	panel := panelStyle.Render(inner)
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panel)
}

// fitHeight pads (or clips) a rendered block to exactly h lines.
func fitHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
