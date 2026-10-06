package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/apply"
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
	// the frame is the window, the stage is the page. At the
	// 80-column floor it is the full canvas — nothing shifts; past
	// that it grows with the terminal up to a comfortable reading
	// width, so wide windows get roomy pages instead of a narrow
	// column floating in space.
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
	livePaused   bool // sampling paused while an audit borrows the connection
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

	// the fleet's applications, read from local state; deploys write
	// them, the TUI reads them. appCursor is the selection on the
	// apps pages.
	apps      []domain.App
	appCursor int

	// the apply flow: the typed confirmation, the engine's report,
	// the verify rows, and the injectable pieces tests replace —
	// the runner and the second-connection prover.
	applyTyped    string
	applyResult   apply.Result
	applyChecks   []preflight.Check
	applyOpts     apply.Options
	applyRunner   preflight.Runner                  // nil: the live connection or a fresh dial
	applyProgress chan string                       // the engine's live progress pump
	netProgress   chan string                       // the dial's live progress pump
	newProver     func(keyPath string) apply.Prover // nil: the real second connection

	// firstContactCancel stops an onboarding in flight: esc during
	// the install cancels the work, not just its ceremony
	firstContactCancel context.CancelFunc

	// auditBusy is set while the preflight audit or plan generation
	// runs, even if the user walked away from its loading screen —
	// live sampling resumes only once the connection is free again.
	auditBusy bool

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

// netProgressMsg carries one line from a dial in flight — a connect
// retry, or the step it just reached.
type netProgressMsg struct {
	line string
}

// netProgressReader is the dial pump's read end: one line per
// message, re-issued the way bubbletea's event loop would.
func (m Model) netProgressReader() tea.Cmd {
	ch := m.netProgress
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return netProgressMsg{line: line}
	}
}

// netSink is the progress func every dialing ceremony hands the
// transport. It never blocks: a dial must not wait on the view, and
// a line that arrives with no reader pumping is dropped, not paid
// for with a hang.
func (m Model) netSink() func(string) {
	ch := m.netProgress
	return func(line string) {
		if ch == nil {
			return
		}
		select {
		case ch <- line:
		default:
		}
	}
}

// New builds the workspace model from the given store. Loading failures are
// surfaced inside the UI, not here.
func New(store *state.Store) Model {
	m := Model{
		store:       store,
		netProgress: make(chan string, 8),
		stack:       []screen{{kind: scHome}},
		help:        help.New(),
		fullHelp:    help.New(),
		width:       80,
		height:      24,
	}
	m.fullHelp.ShowAll = true
	if store != nil {
		if nodes, err := store.LoadNodes(); err == nil {
			m.nodes = nodes
		} else {
			m.loadErr = err
		}
		if apps, err := store.LoadApps(); err == nil {
			m.apps = apps
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
	m.actions = newActionsList(domain.Node{})
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

	case firstContactDoneMsg:
		return m.handleFirstContactDone(msg)

	case sshFinishedMsg:
		return m.handleSSHFinished(msg)

	case liveSampleMsg:
		return m.handleLiveSample(msg)

	case liveErrMsg:
		return m.handleLiveErr(msg)

	case pfDoneMsg:
		next, cmd := m.handlePreflightDone(msg)
		return withResume(next, cmd)

	case planMsg:
		next, cmd := m.handlePlanDone(msg)
		return withResume(next, cmd)

	case netProgressMsg:
		// one dial retry line: the loading card's detail is where a
		// booting or slow box reads as "still trying", never as a hang
		if m.loading.active && msg.line != "" {
			m.loading.detail = msg.line
		}
		return m, m.netProgressReader()

	case applyProgressMsg:
		if m.loading.active && m.cur().kind == scNodeApplyReport && msg.line != "" {
			m.loading.detail = msg.line
		}
		return m, m.applyProgressReader()

	case applyDoneMsg:
		next, cmd := m.handleApplyDone(msg)
		return withResume(next, cmd)

	case verifyDoneMsg:
		next, cmd := m.handleVerifyDone(msg)
		return withResume(next, cmd)

	case tea.KeyPressMsg:
		if !m.introDone {
			return m.updateIntro(msg)
		}
		next, cmd := m.updateKeys(msg)
		return withResume(next, cmd)
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

// withResume restarts live sampling when the observe page just became
// the resting screen and the connection is free — the user came back
// from an audit that borrowed the live session's connection.
func withResume(next tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	mm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if rc := mm.maybeResumeLive(); rc != nil {
		if cmd == nil {
			cmd = rc
		} else {
			cmd = tea.Batch(cmd, rc)
		}
	}
	return mm, cmd
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

	// h is home from anywhere: one fixed way back, on every page —
	// forms own their keys, and a ceremony in flight is not left
	// behind mid-act (esc and q still serve there)
	if str == "h" && !m.formActive() && !m.loading.active && m.introDone &&
		m.cur().kind != scHome {
		m.stopLive()
		m.confirmRemove = false
		m.stack = []screen{{kind: scHome}}
		return m, nil
	}

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
		// the plan page is the confirmation: runes build the name
		return m.updatePlanKeys(str)
	case scNodeApplyReport:
		if m.loading.active {
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "esc":
				// the work keeps running in the background; its
				// result still records when it lands
				m.cancelLoading()
				return m, nil
			}
			return m, nil
		}
		switch str {
		case "q":
			return m, tea.Quit
		case "esc":
			m.pop()
			// the preflight review that led here is answered by the
			// report itself; home is the observe page
			if m.cur().kind == scNodePreflight {
				m.pop()
			}
			return m, nil
		}
		return m, nil
	case scAddNode:
		switch m.addNode.stage {
		case anForm:
			return m, nil // text keys go to the embedded form
		case anInstalling:
			return m.updateInstalling(str)
		}
		return m.updateAddReview(str)
	case scApps, scNodeApps:
		return m.updateAppsKeys(s, str)
	case scAppDetail:
		return m.updateSimpleKeys(s.kind, str)
	default:
		return m.updateSimpleKeys(s.kind, str)
	}
}

// updateAppsKeys walks the applications list and opens the selected
// app's detail page. The record is the source — the pages are the
// fleet's eyes; the CLI holds the hands.
func (m Model) updateAppsKeys(s screen, str string) (tea.Model, tea.Cmd) {
	apps := m.visibleApps(s)
	switch str {
	case "q":
		return m, tea.Quit
	case "esc":
		m.pop()
		return m, nil
	case "up", "k":
		if m.appCursor > 0 {
			m.appCursor--
		}
		return m, nil
	case "down", "j":
		if m.appCursor < len(apps)-1 {
			m.appCursor++
		}
		return m, nil
	case "enter":
		if m.appCursor < len(apps) {
			picked := apps[m.appCursor]
			m.push(screen{kind: scAppDetail, node: picked.Node, app: picked.Name})
			return m, nil
		}
	}
	return m, nil
}

// visibleApps is what an apps screen shows: everything on the fleet
// page, one node's slice on the node page.
func (m Model) visibleApps(s screen) []domain.App {
	if s.kind != scNodeApps {
		return m.apps
	}
	var out []domain.App
	for _, a := range m.apps {
		if a.Node == s.node {
			out = append(out, a)
		}
	}
	return out
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
		m.reloadApps()
		m.appCursor = 0
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
		m.reloadApps()
		m.appCursor = 0
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
		// the shortcut exists exactly when the action does: a
		// managed node has no setup to offer and no p to press
		if !nodeIsManaged(m.selNode) {
			return m.runPreflight()
		}
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
		name := m.selNode.Name
		host, port := m.selNode.Host, m.selNode.Port
		// removal is mymo forgetting its whole side: the record, the
		// key pair, and the trust entry — the box itself keeps
		// everything mymo built there, running and untouched
		if err := m.store.RemoveNode(name); err != nil {
			m.confirmRemove = false
			return m, m.notify(shorten(err.Error(), 44), toastErr)
		}
		if err := ssh.ForgetHostKey(filepath.Join(m.store.Dir(), "known_hosts.json"), host, port); err != nil {
			m.confirmRemove = false
			return m, m.notify("the node was removed but a trust entry stayed: "+shorten(err.Error(), 30), toastErr)
		}
		m.confirmRemove = false
		m.pop()
		m.reloadFleet()
		return m, m.notify("removed "+name+" — mymo forgot it; the box was not touched", toastOK)
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
		m.reloadApps()
		m.appCursor = 0
		m.push(screen{kind: scNodeApps, node: m.selNode.Name})
	case actInspect:
		m.push(screen{kind: scNodeInspect, node: m.selNode.Name})
	case actRemove:
		m.confirmRemove = true
	case actSetup:
		return m.runPreflight()
	}
	return m, nil
}

// openNode enters a node's observe page: entering observes. A fresh
// probe starts with the loading page up; its connection is adopted
// for live stats when the snapshot lands.
func (m *Model) openNode(n domain.Node) tea.Cmd {
	m.stopLive()
	m.selNode = n
	m.actions = newActionsList(n)
	// the rebuilt list must be sized to the window, not its default
	m.actions.SetSize(m.contentWidth, len(actionDefs(n))+1)
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

// reloadApps rereads the applications record — deploys and rollbacks
// change it from the CLI while the TUI lives, so the apps screens
// always re-ask the store on entry.
func (m *Model) reloadApps() {
	if m.store == nil {
		return
	}
	if apps, err := m.store.LoadApps(); err == nil {
		m.apps = apps
	}
}

// layout recomputes the frame and sizes all widgets to the window.
// The signature frame reserves its two border rows plus the sticky header
// and divider; the in-frame footer takes one more row inside. What
// remains is the screen content area.
func (m *Model) layout() {
	m.contentWidth = max(1, m.width-2)
	m.stageW = min(m.contentWidth, 110)
	// Chromeless pages (home, intro) keep two border rows and one
	// footer row; header pages add the header and its divider.
	rows := 3
	if m.showHeader() {
		rows = 5
	}
	m.contentHeight = max(1, m.height-rows)
	fleetH := m.contentHeight - 5
	if fleetH < 1 {
		fleetH = 1
	}
	m.fleet.SetSize(m.cardW(), fleetH)
	// the list's own length is the truth: setup adds and removes a
	// row as the box's state changes
	m.actions.SetSize(m.stageW, len(m.actions.Items())+1)
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
		case scNodeApplyReport:
			if m.loading.active {
				content = m.loadingView()
			} else {
				content = m.applyReportView()
			}
		case scApps:
			content = m.allAppsView()
		case scAppDetail:
			content = m.appDetailView(s)
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
		// the home hint rides every footer where h is live: never
		// during forms, ceremonies, or on the home page itself
		km := help.KeyMap(m.keymap())
		if !m.formActive() && !m.loading.active && m.introDone && m.cur().kind != scHome {
			km = keymapWithHome{inner: m.keymap()}
		}
		row = m.help.View(km)
	}
	return lipgloss.PlaceHorizontal(m.contentWidth, lipgloss.Center, row)
}

// statusTextPlain summarizes the fleet: node and application counts.
func (m Model) statusTextPlain() string {
	nodeWord := "nodes"
	if len(m.nodes) == 1 {
		nodeWord = "node"
	}
	appWord := "applications"
	if len(m.apps) == 1 {
		appWord = "application"
	}
	return fmt.Sprintf("%d %s · %d %s", len(m.nodes), nodeWord, len(m.apps), appWord)
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
		km := newNodeKeymap()
		km.setup.SetEnabled(!nodeIsManaged(m.selNode))
		return km
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
	case scNodeApplyReport:
		if m.loading.active {
			return newLoadingKeymap()
		}
		return newApplyReportKeymap()
	case scAddNode:
		switch m.addNode.stage {
		case anForm:
			return newFormKeymap()
		case anInstalling:
			return newLoadingKeymap()
		}
		return newAddReviewKeymap()
	case scApps, scNodeApps:
		return newAppsKeymap()
	case scAppDetail:
		return newSimpleKeymap()
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
