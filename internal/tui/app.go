package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
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
	// stageW is the centered stage column every page renders into:
	// the frame is the window, the stage is the page. It matches the
	// card grid's outer width, so at the 80-column floor it is the
	// full canvas — nothing shifts — and on wide terminals the whole
	// app column centers instead of hugging the left edge.
	stageW        int
	contentHeight int

	helpOpen      bool
	confirmRemove bool
	selNode       domain.Node
	loadErr       error
	toast         *toast

	// loading describes the running loading ceremony — mymo's
	// chromeless splash with a spinner. Entering a node observes it:
	// the probe runs under this ceremony, its connection then adopted
	// for live stats. checkSeq identifies the entry that started it.
	loading  loadingState
	checkSeq int
	spinner  spinner.Model
	// live stats state (node screen). seq identifies the sampling
	// session so messages from a stopped one are ignored.
	live         bool
	liveSeq      int
	liveClient   *ssh.Client
	liveErr      string
	liveCur      liveSample
	livePrev     liveSample
	liveCPU      int // -1 until two samples allow a delta
	liveSparkCPU []int
	liveSparkMem []int

	// preflight results for the open audit screen; re-entering the
	// flow refreshes them
	pfChecks  []preflight.Check
	pfVerdict preflight.Outcome
	pfSnap    facts.Node
	pfAudit   preflight.Audit

	// the generated plan awaiting confirmation
	planSteps []plan.Step

	addNode addNodeState

	descShown int  // letters of the description revealed so far
	descAnim  bool // whether the typewriter is running

	introDone bool
	introStep int
	introErr  string
}

// loadingState describes a running loading ceremony: mymo's splash —
// mark, word, spinner — with what is running and for how long. The
// same component hosts any wait mymo owes the user a face for;
// entering a node to observe it is the first one.
type loadingState struct {
	active bool
	line   string // "observing abed-prod-01 over SSH"
	detail string // the quiet line under the spinner
	node   string // fleet row to mark while it runs
	start  time.Time
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
	m.liveCPU = -1
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
		if m.loading.active {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case checkDoneMsg:
		return m.handleCheckDone(msg)

	case sshFinishedMsg:
		return m.handleSSHFinished(msg)

	case liveSampleMsg:
		return m.handleLiveSample(msg)

	case liveErrMsg:
		return m.handleLiveErr(msg)

	case pfDoneMsg:
		return m.handlePreflightDone(msg)

	case planMsg:
		return m.handlePlanDone(msg)

	case tea.KeyPressMsg:
		if !m.introDone {
			return m.updateIntro(msg)
		}
		return m.updateKeys(msg)
	}
	// Non-key messages (mouse, internal) go to the active screen's widget.
	return m.forward(msg)
}

// cancelLoading ends the running ceremony and drops back one screen.
// The work it was fronting keeps running — its result is still
// recorded when it lands.
func (m *Model) cancelLoading() {
	m.loading = loadingState{}
	m.fleetSetChecking("")
	m.pop()
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
	case scNodePreflight:
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
		if str == "enter" {
			return m.runPlan()
		}
		return m.updateSimpleKeys(s.kind, str)
	case scNodePlan:
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
		return m.updateSimpleKeys(s.kind, str)
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
			cmd := m.openNode(item.node)
			return m, cmd
		}
		return m, nil
	}
	return m.forward(msg)
}

// updateNodeKeys handles keys on the observe page. While a probe is
// running the loading page owns the keys: esc cancels the visit, q
// quits; everything else waits.
func (m Model) updateNodeKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.stopLive()
		m.pop()
		return m, nil
	case "s":
		return m.runSSH()
	case "p":
		return m.runPreflight()
	case "enter":
		if a, ok := m.actions.SelectedItem().(actionItem); ok {
			return m.runAction(a)
		}
		return m, nil
	}
	return m.forward(msg)
}

// updateSimpleKeys handles keys on informational screens.
func (m Model) updateSimpleKeys(_ screenKind, str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
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

// runAction executes an observe-page action.
func (m Model) runAction(a actionItem) (tea.Model, tea.Cmd) {
	switch a.id {
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

// openNode enters a node's observe page: entering observes. A fresh
// probe starts with the loading page up; its connection is adopted
// for live stats when the snapshot lands.
func (m *Model) openNode(n domain.Node) tea.Cmd {
	m.stopLive()
	m.selNode = n
	m.actions = newActionsList()
	// the rebuilt list must be sized to the window, not its default
	m.actions.SetSize(m.contentWidth, len(actionDefs())+1)
	m.push(screen{kind: scNode, node: n.Name})
	m.loading = loadingState{
		active: true,
		line:   "observing " + n.Name + " over SSH",
		detail: "ten read-only commands · nothing is modified",
		node:   n.Name,
		start:  time.Now(),
	}
	m.checkSeq++
	m.fleetSetChecking(n.Name)
	// the ceremony is chromeless: geometry follows it into the larger
	// canvas so the splash fills the window without a dead gap below
	m.layout()
	return tea.Batch(m.spinner.Tick, m.beginCheck(n, m.checkSeq))
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
	m.stageW = min(m.contentWidth, 78)
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
	m.fleet.SetSize(m.stageW, fleetH)
	m.actions.SetSize(m.stageW, len(actionDefs())+1)
	m.help.SetWidth(m.stageW)
	m.fullHelp.SetWidth(max(10, m.stageW-8))
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
		content = m.frame(m.introView(), "")
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
			switch {
			case m.loading.active:
				content = m.loadingView()
			case m.confirmRemove:
				content = m.removeConfirmView()
			default:
				content = m.nodeView()
			}
		case scNodeApps:
			content = m.nodeAppsView(s.node)
		case scNodeInspect:
			content = m.inspectView()
		case scNodePreflight:
			if m.loading.active {
				content = m.loadingView()
			} else {
				content = m.preflightView()
			}
		case scNodePlan:
			if m.loading.active {
				content = m.loadingView()
			} else {
				content = m.planView()
			}
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
	return m.frame(content, m.footerRow())
}

// showHeader reports whether the sticky header belongs on the current
// screen. Home is the identity itself and carries no chrome; neither
// does the intro — nor a loading ceremony, which is the same splash
// family: mymo's mark and word carry the identity while the work
// runs, and the breadcrumb waits below.
func (m Model) showHeader() bool {
	return m.introDone && m.cur().kind != scHome && !m.loading.active
}

// frame wraps the page in the signature mymo border, centering it on
// the stage. The footer is fitted as its own last row: it is always
// full-width, and measuring it together with the page would defeat
// the page's centering. Inner pages hang their identity from the
// sticky header: icon, brand, breadcrumb. Home and the intro skip
// it — their content is the identity — leaving just the canvas.
func (m Model) frame(page, footer string) string {
	innerW := max(1, m.width-2)
	innerH := max(1, m.height-2)
	contentH := innerH
	if m.showHeader() {
		contentH = max(1, innerH-2) // header and divider rows
	}
	pageH := contentH
	if footer != "" {
		pageH = max(1, contentH-1)
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
	for _, line := range fitLines(centerBlock(page, innerW), innerW, pageH) {
		b.WriteString(frameStyle.Render("│"))
		b.WriteString(line)
		b.WriteString(frameStyle.Render("│"))
		b.WriteString("\n")
	}
	for _, line := range fitLines(footer, innerW, 1) {
		b.WriteString(frameStyle.Render("│"))
		b.WriteString(line)
		b.WriteString(frameStyle.Render("│"))
		b.WriteString("\n")
	}
	b.WriteString(m.bottomBorder(innerW))
	return b.String()
}

// topBorder renders the plain top frame line.
func (m Model) topBorder(w int) string {
	return frameStyle.Render("╭" + strings.Repeat("─", w) + "╮")
}

// headerRow renders the sticky header: mymo's icon and brand with the
// breadcrumb of the current screen, centered like the stage below it.
// The icon sits one cell in from the frame so its blocks never touch
// the border.
func (m Model) headerRow(w int) string {
	line := " " + brandIcon() + " " + brandStyle.Render("mymo")
	for _, c := range m.crumbs() {
		line += faintStyle.Render(" / ") + subtextStyle.Render(c)
	}
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, line)
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

// footerRow renders the in-frame footer: the current screen's key
// help, centered like the stage. An active toast takes the row
// instead — one consistent home for every notice, never a line bolted
// onto the header, and it never steals a row from the content.
func (m Model) footerRow() string {
	var row string
	if m.toast != nil {
		row = m.toast.view()
	} else {
		row = m.help.View(m.keymap())
	}
	return lipgloss.PlaceHorizontal(m.contentWidth, lipgloss.Center, row)
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
		if m.loading.active {
			return newLoadingKeymap()
		}
		if m.confirmRemove {
			return newConfirmKeymap()
		}
		return newNodeKeymap()
	case scNodePreflight:
		if m.loading.active {
			return newLoadingKeymap()
		}
		km := newPreflightKeymap()
		// enter is only honest advice when the verdict allows it; a
		// blocked audit drops the key from the footer, not from the
		// screen
		km.plan.SetEnabled(m.pfVerdict == preflight.Pass || m.pfVerdict == preflight.Adopt)
		return km
	case scNodePlan:
		if m.loading.active {
			return newLoadingKeymap()
		}
		return newPlanKeymap()
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

// centerBlock centers a rendered page as one unit: every line gets
// the same left gutter so the page keeps its own internal alignment
// while the block floats in the frame. Line-by-line centering would
// scatter short headings away from the cards below them.
func centerBlock(s string, width int) string {
	lines := strings.Split(s, "\n")
	blockW := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > blockW {
			blockW = w
		}
	}
	if blockW >= width {
		return s
	}
	pad := strings.Repeat(" ", (width-blockW)/2)
	for i := range lines {
		lines[i] = pad + lines[i]
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
