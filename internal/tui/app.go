package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
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

	// probe state: one check runs at a time, animating the node
	// screen and marking the fleet row until checkDoneMsg lands.
	probing     bool
	probingName string
	spinner     spinner.Model

	addNode addNodeState

	descShown int  // letters of the description revealed so far
	descAnim  bool // whether the typewriter is running

	introDone bool
	introStep int
	introErr  string
}

// New builds the workspace model from the given store. Loading failures are
// surfaced inside the UI, not here.
func New(store *state.Store) Model {
	m := Model{
		store:    store,
		stack:    []screen{{kind: scHome}},
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
	// A returning user lands on home with the description already
	// typing; a first-timer meets the intro first.
	m.descAnim = m.introDone
	m.fleet = newFleetList(m.nodes)
	m.actions = newActionsList()
	m.spinner = newCheckSpinner()
	applyHelpPalette(&m.help)
	applyHelpPalette(&m.fullHelp)
	m.layout()
	return m
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	if !m.introDone {
		return m.nextIntroTick()
	}
	return animDesc()
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

	case descTickMsg:
		if m.descAnim {
			cmd := m.advanceDesc()
			return m, cmd
		}
		return m, nil

	case spinner.TickMsg:
		if m.probing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case checkDoneMsg:
		return m.handleCheckDone(msg)

	case sshFinishedMsg:
		return m.handleSSHFinished(msg)

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
	case scHome:
		return m.updateHomeKeys(msg)
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

// updateHomeKeys handles keys on the home hub: small letters navigate,
// capital N creates a node.
func (m Model) updateHomeKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "n":
		m.push(screen{kind: scFleet})
		if m.fleetEmpty() {
			cmd := m.beginDesc()
			return m, cmd
		}
		return m, nil
	case "N":
		return m.addNodeScreen()
	case "a":
		m.push(screen{kind: scApps})
		return m, nil
	case "d":
		m.push(screen{kind: scDeploy})
		return m, nil
	case "s":
		m.push(screen{kind: scSettings})
		return m, nil
	}
	return m, nil
}

// addNodeScreen opens the add-node workflow with the fleet beneath it,
// so saving or aborting lands on the node list.
func (m Model) addNodeScreen() (tea.Model, tea.Cmd) {
	if m.cur().kind != scFleet {
		m.push(screen{kind: scFleet})
	}
	return m.startAddNode()
}

// fleetEmpty reports whether the node list has nothing to show.
func (m Model) fleetEmpty() bool {
	return len(m.nodes) == 0
}

// updateFleetKeys handles keys on the fleet screen.
func (m Model) updateFleetKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "N":
		return m.addNodeScreen()
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

// updateNodeKeys handles keys on the node overview screen: the action
// list, plus check and ssh as direct keys.
func (m Model) updateNodeKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "c":
		return m.runCheck()
	case "s":
		return m.runSSH()
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
	case actCheck:
		return m.runCheck()
	case actSSH:
		return m.runSSH()
	case actApps:
		m.push(screen{kind: scNodeApps, node: m.selNode.Name})
	case actInspect:
		m.push(screen{kind: scNodeInspect, node: m.selNode.Name})
	case actRemove:
		m.confirmRemove = true
	}
	return m, nil
}

// openNode focuses a node: pushes its screen and rebuilds the action list.
func (m *Model) openNode(n domain.Node) {
	m.selNode = n
	m.actions = newActionsList()
	// the rebuilt list must be sized to the window, not its default
	m.actions.SetSize(m.contentWidth, len(actionDefs())+1)
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
// The signature frame reserves its two border rows plus the sticky header
// and divider; the in-frame footer takes one more row inside. What
// remains is the screen content area.
func (m *Model) layout() {
	m.contentWidth = max(1, m.width-2)
	// Chromeless pages (home, intro) keep two border rows and one
	// footer row; header pages add the header and its divider.
	rows := 3
	if m.showHeader() {
		rows = 5
	}
	m.contentHeight = max(1, m.height-rows)
	fleetH := m.contentHeight - 4
	if fleetH < 1 {
		fleetH = 1
	}
	m.fleet.SetSize(m.contentWidth, fleetH)
	m.actions.SetSize(m.contentWidth, len(actionDefs())+1)
	m.help.SetWidth(m.contentWidth)
	m.fullHelp.SetWidth(max(10, m.contentWidth-8))
	if m.addNode.form != nil {
		w := m.contentWidth - 12 // card border and padding
		if w > 72 {
			w = 72
		}
		if w < 30 {
			w = 30
		}
		// The form renders its fields plus its in-card help line inside
		// this many rows; an explicit height keeps the help visible and
		// the card compact. Update when adding fields.
		m.addNode.form = m.addNode.form.WithWidth(w).WithHeight(14)
	}
}

// View satisfies tea.Model.
func (m Model) View() tea.View {
	var content string
	if !m.introDone {
		content = m.frame(m.introView())
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
		case scHome:
			content = m.homeView()
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
	return m.frame(content + "\n" + m.footerRow())
}

// showHeader reports whether the sticky header belongs on the current
// screen. Home is the identity itself and carries no chrome; neither
// does the intro, which is the same splash family.
func (m Model) showHeader() bool {
	return m.introDone && m.cur().kind != scHome
}

// frame wraps content in the signature mymo border. Inner pages hang
// their identity from the sticky header: icon, brand, breadcrumb. Home
// and the intro skip it — their content is the identity — leaving just
// the canvas.
func (m Model) frame(content string) string {
	innerW := max(1, m.width-2)
	innerH := max(1, m.height-2)
	contentH := innerH
	if m.showHeader() {
		contentH = max(1, innerH-2) // header and divider rows
	}
	var b strings.Builder
	b.WriteString(m.topBorder(innerW))
	b.WriteString("\n")
	if m.showHeader() {
		b.WriteString(m.headerRow(innerW))
		b.WriteString("\n")
		b.WriteString(dividerStyle.Render(strings.Repeat("─", innerW)))
		b.WriteString("\n")
	}
	for _, line := range fitLines(content, innerW, contentH) {
		b.WriteString(frameStyle.Render("│"))
		b.WriteString(line)
		b.WriteString(frameStyle.Render("│"))
		b.WriteString("\n")
	}
	b.WriteString(m.bottomBorder(innerW))
	return b.String()
}

// topBorder renders the plain top frame line; on chromeless pages an
// active toast rides the border's right end.
func (m Model) topBorder(w int) string {
	plain := "╭" + strings.Repeat("─", w) + "╮"
	if m.showHeader() || m.toast == nil {
		return frameStyle.Render(plain)
	}
	t := m.toast.view()
	dashes := w - lipgloss.Width(t) - 4
	if dashes < 1 {
		return frameStyle.Render(plain)
	}
	return frameStyle.Render("╭" + strings.Repeat("─", dashes) + " " + t + " ╮")
}

// headerRow renders the sticky header: mymo's icon and brand with the
// breadcrumb of the current screen, and any active toast at the right.
// The icon sits one cell in from the frame so its blocks never touch
// the border.
func (m Model) headerRow(w int) string {
	left := " " + brandIcon() + " " + brandStyle.Render("mymo")
	for _, c := range m.crumbs() {
		left += faintStyle.Render(" / ") + subtextStyle.Render(c)
	}
	if m.toast == nil {
		return left
	}
	t := m.toast.view()
	if gap := w - lipgloss.Width(left) - lipgloss.Width(t); gap > 0 {
		return left + strings.Repeat(" ", gap) + t
	}
	return left
}

// bottomBorder renders the bottom frame line: the fleet status at the
// left, the version at the right.
func (m Model) bottomBorder(w int) string {
	left := faintStyle.Render(" " + m.statusTextPlain() + " ")
	right := faintStyle.Render(" mymo " + version.Version + " ")
	fill := w - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 0 {
		// not enough room: the version wins, the status drops
		left = ""
		fill = w - lipgloss.Width(right)
		if fill < 0 {
			fill = 0
		}
	}
	return frameStyle.Render("╰") + left +
		frameStyle.Render(strings.Repeat("─", fill)) + right +
		frameStyle.Render("╯")
}

// footerRow renders the in-frame footer: the current screen's key help.
func (m Model) footerRow() string {
	return m.help.View(m.keymap())
}

// statusTextPlain summarizes the fleet: node and application counts.
func (m Model) statusTextPlain() string {
	nodeWord := "nodes"
	if len(m.nodes) == 1 {
		nodeWord = "node"
	}
	return fmt.Sprintf("%d %s · 0 applications", len(m.nodes), nodeWord)
}

// keymap returns the help keymap for the current screen.
func (m Model) keymap() help.KeyMap {
	switch s := m.cur(); s.kind {
	case scHome:
		return newHomeKeymap()
	case scFleet:
		return newFleetKeymap()
	case scNode:
		if m.confirmRemove {
			return newConfirmKeymap()
		}
		return newNodeKeymap()
	case scAddNode:
		if m.addNode.stage == anForm {
			return newFormKeymap()
		}
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

// fitLines pads (or clips) a block to exactly h lines of exactly w visible
// columns, so the frame's side borders always align.
func fitLines(s string, w, h int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, line := range lines {
		if gap := w - lipgloss.Width(line); gap > 0 {
			lines[i] = line + strings.Repeat(" ", gap)
		}
	}
	return lines
}
