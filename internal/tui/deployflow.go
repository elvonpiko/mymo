package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/manifest"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// The deploy workflow: the same path the CLI walks — discover the
// project, resolve the target, confirm with the app's name typed
// in full, run the engine with the ceremony narrated, land on the
// report. The TUI adds no steps and skips none.

// dpStageID is one step of the deploy flow.
type dpStageID int

const (
	dpPath    dpStageID = iota // the project directory, typed
	dpPick                     // a new app's node, picked from the verified
	dpConfirm                  // the app's name typed in full
	dpReport                   // the engine's outcome, stated
)

// deployDoneMsg carries the engine's whole result — persisted on
// success and failure alike, because history is history.
type deployDoneMsg struct {
	app      string
	node     string
	res      deploy.Result
	fatal    string
	existing bool
}

// openDeploy starts the flow at the project walker, opened where
// mymo was run from — the same default the CLI uses, but walked,
// not typed.
func (m *Model) openDeploy() {
	m.dpStage = dpPath
	m.dpTyped = ""
	m.dpDraft = ""
	m.dpFound = ""
	m.dpErr = ""
	m.dpMan = manifest.Manifest{}
	m.dpExisting = false
	m.dpResult = deploy.Result{}
	m.dpDoneErr = ""
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	m.dpPathText = cwd
	m.dpLoadWalk()
	m.dpNodeCursor = 0
	m.push(screen{kind: scDeploy})
}

// deployKeymap states the flow's keys for the stage it is in.
func (m Model) deployKeymap() help.KeyMap {
	switch m.dpStage {
	case dpReport:
		return newDeployReportKeymap()
	default:
		return newDeployFlowKeymap()
	}
}

// updateDeployKeys is the flow's keyboard: each stage owns it, and
// while the engine runs the ceremony owns the keys.
func (m Model) updateDeployKeys(str string) (tea.Model, tea.Cmd) {
	if m.loading.active {
		switch str {
		case "q":
			return m, tea.Quit
		case "esc":
			// the release keeps deploying in the background; its
			// result still records when it lands
			m.cancelLoading()
			return m, nil
		}
		return m, nil
	}
	switch m.dpStage {
	case dpPath:
		return m.updateDeployPathKeys(str)
	case dpPick:
		return m.updateDeployPickKeys(str)
	case dpConfirm:
		return m.updateDeployConfirmKeys(str)
	case dpReport:
		return m.updateDeployReportKeys(str)
	}
	return m.updateSimpleKeys(scDeploy, str)
}

// walkEntry is one row of the project walker: a directory, and
// what a deploy would find inside it. The walker is not a file
// manager — directories only, one column, the markers doing the
// finding: recognition instead of recall, and no path typo can
// survive it.
type walkEntry struct {
	name       string
	this, up   bool // the current directory, and the way out
	manifest   bool
	dockerfile bool
}

// dpLoadWalk reads the walker's directory and marks every entry
// with what a deploy would find there. Hidden directories are not
// projects; symlinks are not descended.
func (m *Model) dpLoadWalk() {
	dir := m.dpPathText
	m.dpWalkErr = ""
	entries := []walkEntry{{name: "this directory", this: true}}
	if p := filepath.Dir(dir); p != dir {
		entries = append(entries, walkEntry{name: "..", up: true})
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		m.dpWalkErr = shorten(err.Error(), 60)
		m.dpWalkEntries = entries
		m.dpWalkCursor, m.dpWalkScroll = 0, 0
		return
	}
	names := make([]string, 0, len(des))
	for _, de := range des {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		names = append(names, de.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		e := walkEntry{name: name}
		sub := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(sub, manifest.FileName)); err == nil {
			e.manifest = true
		} else if _, err := os.Stat(filepath.Join(sub, "Dockerfile")); err == nil {
			e.dockerfile = true
		}
		entries = append(entries, e)
	}
	m.dpWalkEntries = entries
	m.dpWalkCursor, m.dpWalkScroll = 0, 0
}

// dpWalkWindowRows is the rows the walker's list holds.
func (m Model) dpWalkWindowRows(scrollable bool) int {
	n := m.contentHeight - 8
	if scrollable {
		n--
	}
	if n < 1 {
		return 1
	}
	return n
}

// updateDeployPathKeys: the walker's keyboard — j/k walks, enter
// opens (a marked directory is the project; a plain one descends),
// u climbs, esc leaves without a trace.
func (m Model) updateDeployPathKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "esc":
		m.pop()
		return m, nil
	case "up", "k":
		if m.dpWalkCursor > 0 {
			m.dpWalkCursor--
			m.dpWalkFollow()
		}
		return m, nil
	case "down", "j":
		if m.dpWalkCursor < len(m.dpWalkEntries)-1 {
			m.dpWalkCursor++
			m.dpWalkFollow()
		}
		return m, nil
	case "u", "backspace":
		return m.dpWalkClimb()
	case "enter":
		if m.dpWalkCursor >= len(m.dpWalkEntries) {
			return m, nil
		}
		e := m.dpWalkEntries[m.dpWalkCursor]
		switch {
		case e.this:
			// the current directory is the project: discovery
			// answers — manifest, draft, or the honest refusal
			return m.discoverProject()
		case e.up:
			return m.dpWalkClimb()
		case e.manifest:
			m.dpPathText = filepath.Join(m.dpPathText, e.name)
			return m.discoverProject()
		default:
			m.dpPathText = filepath.Join(m.dpPathText, e.name)
			m.dpLoadWalk()
			return m, nil
		}
	}
	return m, nil
}

// dpWalkClimb goes one directory up; the root has no up.
func (m Model) dpWalkClimb() (tea.Model, tea.Cmd) {
	p := filepath.Dir(m.dpPathText)
	if p != m.dpPathText {
		m.dpPathText = p
		m.dpLoadWalk()
	}
	return m, nil
}

// dpWalkFollow keeps the cursor inside the window: rows never jump
// arbitrarily, the list follows the walk.
func (m *Model) dpWalkFollow() {
	scrollable := len(m.dpWalkEntries) > m.dpWalkWindowRows(false)
	w := m.dpWalkWindowRows(scrollable)
	if m.dpWalkCursor < m.dpWalkScroll {
		m.dpWalkScroll = m.dpWalkCursor
	}
	if m.dpWalkCursor >= m.dpWalkScroll+w {
		m.dpWalkScroll = m.dpWalkCursor - w + 1
	}
}

// updateDeployPickKeys: the verified app hosts, walked and picked.
func (m Model) updateDeployPickKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "esc":
		m.dpStage = dpPath
		return m, nil
	case "up", "k":
		if m.dpNodeCursor > 0 {
			m.dpNodeCursor--
		}
		return m, nil
	case "down", "j":
		if m.dpNodeCursor < len(m.readyHosts())-1 {
			m.dpNodeCursor++
		}
		return m, nil
	case "enter":
		hosts := m.readyHosts()
		if m.dpNodeCursor >= len(hosts) {
			return m, nil
		}
		return m.enterDeployConfirm(hosts[m.dpNodeCursor].Name)
	}
	return m, nil
}

// updateDeployConfirmKeys: the typed-name gate, the same ceremony
// as the CLI — the exact name runs the release, anything else
// aborts without a trace.
func (m Model) updateDeployConfirmKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "esc":
		m.dpTyped = ""
		if m.dpExisting || len(m.readyHosts()) == 0 {
			m.pop()
			return m, nil
		}
		m.dpStage = dpPick
		return m, nil
	case "enter":
		if m.dpTyped != m.dpMan.Name {
			m.dpTyped = ""
			return m, m.notify("that does not match "+m.dpMan.Name+" — nothing runs until it does", toastWarn)
		}
		return m.runDeployEngine()
	case "backspace":
		if n := utf8.RuneCountInString(m.dpTyped); n > 0 {
			m.dpTyped = string([]rune(m.dpTyped)[:n-1])
		}
		return m, nil
	default:
		if utf8.RuneCountInString(str) == 1 {
			m.dpTyped += str
		}
		return m, nil
	}
}

// updateDeployReportKeys: the finished report exits forward —
// enter opens the application it just made.
func (m Model) updateDeployReportKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "enter":
		for i := range m.apps {
			if m.apps[i].Name == m.dpResult.App.Name {
				m.openApp(m.apps[i])
				return m, nil
			}
		}
		m.reloadApps()
		return m, m.notify(m.dpResult.App.Name+" is not in the record — it may have been removed", toastErr)
	}
	return m.updateSimpleKeys(scDeploy, str)
}

// readyHosts lists the nodes mymo has verified READY — deploy's
// only legal targets.
func (m Model) readyHosts() []domain.Node {
	var hosts []domain.Node
	for _, n := range m.nodes {
		if n.Bootstrap.State == domain.BootstrapReady {
			hosts = append(hosts, n)
		}
	}
	return hosts
}

// discoverProject runs the CLI's discovery on the typed directory:
// mymo.yaml is the truth; a bare Dockerfile earns a draft to
// finish; neither is a refusal mymo will not guess past.
func (m Model) discoverProject() (tea.Model, tea.Cmd) {
	dir := strings.TrimSpace(m.dpPathText)
	if dir == "" {
		m.dpErr = "type the project directory — the one holding mymo.yaml or a Dockerfile"
		return m, nil
	}
	man, draft, found, err := manifest.Discover(dir)
	if err != nil {
		m.dpErr = shorten(err.Error(), m.stageW-12)
		m.dpDraft = ""
		m.dpFound = ""
		if errors.Is(err, manifest.ErrNoSource) {
			m.dpErr = "no mymo.yaml and no Dockerfile here — mymo does not guess"
			m.dpFound = "none"
		}
		return m, nil
	}
	if draft != "" {
		m.dpDraft = draft
		m.dpFound = found
		m.dpErr = ""
		return m, nil
	}
	m.dpMan = man
	m.dpDraft = ""
	m.dpFound = found
	m.dpErr = ""

	m.dpDir = dir

	// the app record: existing apps keep their node and identity;
	// a new app names its node now
	_, err = m.store.GetApp(man.Name)
	m.dpExisting = err == nil
	if err != nil && !errors.Is(err, state.ErrAppNotFound) {
		m.dpErr = shorten(err.Error(), m.stageW-12)
		return m, nil
	}
	if m.dpExisting {
		app, err := m.store.GetApp(man.Name)
		if err != nil {
			m.dpErr = shorten(err.Error(), m.stageW-12)
			return m, nil
		}
		if string(app.Type) != man.Type {
			m.dpErr = fmt.Sprintf("app %q is type %s; changing to %s is not supported — remove and redeploy under a new name",
				app.Name, app.Type, man.Type)
			return m, nil
		}
		// an existing app's node is fixed: straight to the gate
		return m.enterDeployConfirm(app.Node)
	}
	if len(m.readyHosts()) == 0 {
		m.dpErr = "no verified app host in the fleet — run initial setup on a node first"
		return m, nil
	}
	m.dpNodeCursor = 0
	m.dpStage = dpPick
	return m, nil
}

// enterDeployConfirm arms the typed-name gate with the resolved
// target — after the record's own honesty checks pass. The domain
// conflict is refused here, before the dial, before the confirm,
// before any mutation on the node.
func (m Model) enterDeployConfirm(nodeName string) (tea.Model, tea.Cmd) {
	app, err := m.store.GetApp(m.dpMan.Name)
	if err != nil {
		if !errors.Is(err, state.ErrAppNotFound) {
			m.dpErr = shorten(err.Error(), m.stageW-12)
			return m, nil
		}
		app = domain.App{Name: m.dpMan.Name, Node: nodeName,
			Type: domain.AppSource(m.dpMan.Type), AddedAt: time.Now().UTC()}
	} else if nodeName != app.Node {
		// the pick stage passed a different host than the record: a
		// new app only — existing apps never change nodes
		m.dpErr = fmt.Sprintf("app %q lives on node %q — an app does not change nodes", app.Name, app.Node)
		return m, nil
	}
	app.Type = domain.AppSource(m.dpMan.Type)
	app.Image = m.dpMan.Image
	app.Dockerfile = m.dpMan.Dockerfile
	app.Port = m.dpMan.Port
	app.Domain = m.dpMan.Domain
	app.Health = m.dpMan.Health
	if err := app.Validate(); err != nil {
		m.dpErr = shorten(err.Error(), m.stageW-12)
		return m, nil
	}
	if err := m.store.CheckDomainFree(app); err != nil {
		m.dpErr = err.Error()
		return m, m.notify(shorten(err.Error(), 44), toastErr)
	}
	m.dpTargetApp = app
	m.dpTyped = ""
	m.dpStage = dpConfirm
	return m, nil
}

// runDeployEngine runs the release lifecycle behind the loading
// ceremony — the same engine, the same protections, narrated.
func (m Model) runDeployEngine() (tea.Model, tea.Cmd) {
	app := m.dpTargetApp
	node, err := m.store.GetNode(app.Node)
	if err != nil {
		m.dpErr = shorten(err.Error(), m.stageW-12)
		m.dpStage = dpPath
		return m, nil
	}
	if node.Bootstrap.State != domain.BootstrapReady {
		m.dpErr = "node " + node.Name + " is " + bootstrapWord(node.Bootstrap.State) +
			" — mymo cannot deploy to a node it has not verified"
		m.dpStage = dpPath
		return m, nil
	}

	m.loading = loadingState{
		active: true,
		line:   "deploying " + app.Name + " to " + node.Name,
		detail: "build or pull, health gate, activate — the previous release is kept",
		start:  time.Now(),
	}
	m.loadingApp = app.Name
	progress := make(chan string, 64)
	m.appProgress = progress
	name := app.Name
	existing := m.dpExisting

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		var runner deploy.Runner
		var client *ssh.Client
		if m.appRunner != nil {
			runner = m.appRunner
		} else {
			client = ssh.New(node, filepath.Join(m.store.Dir(), "known_hosts.json"))
			client.WithProgress(m.netSink())
			if err := client.Dial(ctx); err != nil {
				close(progress)
				return deployDoneMsg{app: name, node: node.Name, fatal: err.Error()}
			}
			defer client.Close()
			runner = client
		}

		sudo := false
		if out, _, err := runner.Run(ctx, "id", "-u"); err == nil && strings.TrimSpace(out) != "0" {
			sudo = true
		}
		var copier deploy.Copier
		if client != nil {
			copier = tuiCopier{client: client, sudo: sudo}
		}
		opts := deploy.Options{
			Sudo:     sudo,
			Progress: func(line string) { progress <- line },
			Copier:   copier,
		}
		if app.Type == domain.SourceDockerfile {
			if m.dpDir == "" {
				close(progress)
				return deployDoneMsg{app: name, node: node.Name, fatal: "the project directory is not resolved"}
			}
			opts.BuildContext = filepath.Dir(filepath.Join(m.dpDir, app.Dockerfile))
		}
		res := deploy.Deploy(ctx, runner, app, opts)
		close(progress)
		return deployDoneMsg{app: name, node: node.Name, res: res, existing: existing}
	}, m.appProgressReader())
}

// tuiCopier carries build contexts through the ssh transport.
type tuiCopier struct {
	client *ssh.Client
	sudo   bool
	runner deploy.Runner
}

func (c tuiCopier) Copy(ctx context.Context, dest string, tarball io.Reader) error {
	if c.client != nil {
		return c.client.Copy(ctx, dest, tarball, c.sudo)
	}
	return errors.New("no transport for the build context")
}

// handleDeployDone lands the engine's result: the record is written
// for failures too — history is history — and the report states
// where it stopped and which release serves now.
func (m Model) handleDeployDone(msg deployDoneMsg) (tea.Model, tea.Cmd) {
	if m.loading.active {
		m.loading = loadingState{}
	}
	m.appProgress = nil
	m.dpStage = dpReport
	m.dpResult = msg.res
	m.dpDoneErr = msg.fatal

	if msg.fatal == "" {
		var err error
		if msg.existing {
			err = m.store.UpdateApp(msg.res.App)
		} else {
			err = m.store.AddApp(msg.res.App)
		}
		if err != nil {
			m.dpDoneErr = "recording the release: " + err.Error()
		}
	}
	m.reloadApps()
	return m, nil
}

// deployFlowView renders the flow's stage.
func (m Model) deployFlowView() string {
	switch m.dpStage {
	case dpPath:
		return m.deployPathView()
	case dpPick:
		return m.deployPickView()
	case dpConfirm:
		return m.deployConfirmView()
	case dpReport:
		return m.deployReportView()
	}
	return m.deployPathView()
}

// deployPathView: the project walker — where you are, and the
// directories around you marked with what a deploy would find.
// A draft or a refusal replaces the rows until walked away from.
func (m Model) deployPathView() string {
	w := m.stageW

	// a draft to finish, or a refusal, takes the stage until the
	// walk moves on
	if m.dpFound == "Dockerfile" && m.dpDraft != "" {
		inner := "\n" + titleStyle.Render("Deploy") + " " +
			faintStyle.Render("· the project directory holds the truth") + "\n\n" +
			warnStyle.Render("this project has a Dockerfile but no mymo.yaml, and mymo does not guess") + "\n\n" +
			subtextStyle.Render("write this draft to mymo.yaml, finish it, and come back:") + "\n\n" +
			textStyle.Render(strings.ReplaceAll(m.dpDraft, "\n", "\n  ")) + "\n\n" +
			faintStyle.Render("u climbs to the parent · esc backs out")
		return m.wizardPanelFit(padLines(inner, w))
	}

	head := "\n" + titleStyle.Render("Deploy") + " " +
		faintStyle.Render("· find the project") + "\n" +
		faintStyle.Render(walkCrumb(m.dpPathText, w-12)) + "\n\n"

	inner := head
	taken := 0
	scrollable := len(m.dpWalkEntries) > m.dpWalkWindowRows(false)
	window := m.dpWalkWindowRows(scrollable)
	for i := m.dpWalkScroll; i < len(m.dpWalkEntries) && taken < window; i++ {
		if taken > 0 {
			inner += "\n"
		}
		inner += m.walkRow(m.dpWalkEntries[i], i == m.dpWalkCursor, w)
		taken++
	}
	if taken == 0 {
		inner += faintStyle.Render("(nothing to walk here)")
	}
	inner += "\n\n"
	if m.dpErr != "" {
		for _, line := range wrapPlain(m.dpErr, w-12) {
			inner += errStyle.Render(line) + "\n"
		}
	}
	inner += faintStyle.Render("enter opens · u climbs · esc backs out")
	if scrollable {
		inner += " " + faintStyle.Render("· j/k walk")
	}
	return fitHeight(padLines(inner, w), m.contentHeight)
}

// walkRow renders one walker row: the name, and what lives inside.
func (m Model) walkRow(e walkEntry, selected bool, w int) string {
	cursor, style := "  ", itemStyle
	if selected {
		cursor, style = "> ", itemSelStyle
	}
	name := e.name
	if e.this {
		name = "this directory"
	}
	marker := ""
	switch {
	case e.manifest:
		marker = okStyle.Render("mymo.yaml")
	case e.dockerfile:
		marker = subtextStyle.Render("Dockerfile — draft")
	case e.up:
		marker = faintStyle.Render("up one")
	}
	nameW := w - 6 - lipgloss.Width(marker) - 4
	return cursor + style.Render(shorten(name, nameW)) +
		strings.Repeat(" ", max(1, w-4-lipgloss.Width(marker)-utf8.RuneCountInString(shorten(name, nameW)))) + marker
}

// walkCrumb shows the walker's directory with the tail kept — the
// tail names the project, the head is the same /home prefix
// everywhere.
func walkCrumb(dir string, w int) string {
	shown := dir
	if utf8.RuneCountInString(shown) > w {
		shown = "\u2026" + string([]rune(shown)[utf8.RuneCountInString(shown)-(w-1):])
	}
	return shown
}

// deployPickView: the verified app hosts — deploy's only targets.
func (m Model) deployPickView() string {
	hosts := m.readyHosts()
	inner := titleStyle.Render("Deploy") + " " +
		faintStyle.Render("· "+m.dpMan.Name+" is new — it needs its node") + "\n\n" +
		subtextStyle.Render("a node mymo has verified READY; an app does not change nodes later") + "\n\n"
	for i, n := range hosts {
		cursor, style := "  ", itemStyle
		if i == m.dpNodeCursor {
			cursor, style = "> ", itemSelStyle
		}
		inner += cursor + style.Render(n.Name) + "  " + faintStyle.Render(n.Host) + "\n"
	}
	inner += "\n" + faintStyle.Render("enter picks · esc backs to the path")
	return m.wizardPanelFit(padLines(inner, m.stageW))
}

// deployConfirmView: the app's whole deployable truth and the name
// typed in full as the human confirmation — the domain promise is
// on the card, because the card is what gets confirmed.
func (m Model) deployConfirmView() string {
	app := m.dpTargetApp
	expose := "internal only — invisible from the internet"
	exposeStyle := subtextStyle
	if app.Domain != "" {
		expose = "https://" + app.Domain + " through caddy"
		exposeStyle = textStyle
	}
	artifact := app.Image
	if app.Type == domain.SourceDockerfile {
		artifact = "Dockerfile in " + app.Dockerfile
	}

	inner := titleStyle.Render("Deploy") + " " +
		faintStyle.Render("· "+app.Name+" → "+app.Node) + "\n\n" +
		factsLabelStyle.Width(10).Render("type") + textStyle.Render(string(app.Type)+" · "+artifact) + "\n" +
		factsLabelStyle.Width(10).Render("exposure") + exposeStyle.Render(expose) + "\n"
	if app.Domain != "" {
		inner += factsLabelStyle.Width(10).Render("dns") + subtextStyle.Render(
			"point "+app.Domain+" at this box first — mymo reaches it at "+m.deployNodeHost(app.Node)) + "\n"
	}
	inner += factsLabelStyle.Width(10).Render("port") + subtextStyle.Render(fmt.Sprint(app.Port)+" · joined to mymo's network") + "\n"
	if app.Health != "" {
		inner += factsLabelStyle.Width(10).Render("health") + subtextStyle.Render(app.Health) + "\n"
	}
	if rel, ok := app.ActiveRelease(); ok {
		inner += factsLabelStyle.Width(10).Render("current") + subtextStyle.Render("release "+fmt.Sprint(rel.ID)+" · kept, ready to roll back") + "\n"
	}
	inner += "\n" + subtextStyle.Render("the previous release is kept — a rollback is one command away") + "\n\n"

	typed := m.dpTyped
	cursor := accentStyle.Render("\u258c")
	if typed == app.Name {
		cursor = okStyle.Render("\u258c")
	}
	inner += subtextStyle.Render("type ") + accentStyle.Render(app.Name) +
		subtextStyle.Render(" in full to deploy \u2014 anything else aborts") + "\n" +
		accentStyle.Render("> ") + textStyle.Render(typed) + cursor
	return m.wizardPanelFit(padLines(inner, m.stageW))
}

// deployNodeHost names the address mymo dials the node at — the
// one DNS should agree with. An unknown host says so honestly.
func (m Model) deployNodeHost(name string) string {
	for _, n := range m.nodes {
		if n.Name == name {
			return n.Host
		}
	}
	return "its recorded address"
}

// deployReportView states the outcome one step per row — the same
// report the CLI prints, ending forward: enter opens the app.
func (m Model) deployReportView() string {
	if m.dpDoneErr != "" {
		return m.wizardPanelFit(padLines(
			titleStyle.Render("Deploy")+" "+faintStyle.Render("· "+m.dpResult.App.Name)+"\n\n"+
				errStyle.Render(m.dpDoneErr)+"\n\n"+
				faintStyle.Render("enter opens the app · h home"),
			m.stageW))
	}

	inner := titleStyle.Render("Deploy") + " " +
		faintStyle.Render("· "+m.dpResult.App.Name+" · "+m.dpResult.App.Node) + "\n\n"
	for _, s := range m.dpResult.Steps {
		word, style := "DONE", okStyle
		switch s.State {
		case deploy.StepKept:
			word, style = "KEPT", subtextStyle
		case deploy.StepFailed:
			word, style = "FAILED", errStyle
		case deploy.StepBlocked:
			word, style = "BLOCKED", warnStyle
		}
		row := style.Width(8).Render(word) + textStyle.Render(s.Title)
		if s.Note != "" {
			row += " " + subtextStyle.Render(shorten(s.Note, m.stageW-10-lipgloss.Width(s.Title)))
		}
		inner += row + "\n"
	}
	inner += "\n"
	if m.dpResult.OK {
		rel, _ := m.dpResult.App.ActiveRelease()
		line := okStyle.Render(m.dpResult.App.Name + " release " + fmt.Sprint(rel.ID) + " is live on " + m.dpResult.App.Node)
		if m.dpResult.App.Domain != "" {
			line += " " + faintStyle.Render("· https://"+m.dpResult.App.Domain)
			inner += line + "\n" +
				faintStyle.Render("tls is issued once the name resolves to this box — caddy keeps retrying on its own") + "\n"
		} else {
			inner += line + "\n"
		}
		inner += faintStyle.Render("enter opens the app")
	} else if _, serving := m.dpResult.App.ActiveRelease(); serving {
		inner += errStyle.Render("the deploy failed — the current release keeps serving") + "\n" +
			faintStyle.Render("enter opens the app · fix what the rows above name, then deploy again")
	} else {
		inner += errStyle.Render("the deploy failed — nothing was activated") + "\n" +
			faintStyle.Render("enter opens the app · fix what the rows above name, then deploy again")
	}
	return m.wizardPanelFit(padLines(inner, m.stageW))
}
