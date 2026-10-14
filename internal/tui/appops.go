package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/facts"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// The application detail page is operational: the record above, the
// verbs below. The CLI's exact commands run over the same transport
// — the TUI surfaces them, it does not reimplement them.

// appActionDefs lists the verbs an app's active release earns. An
// app with nothing live has nothing to operate — the card says so
// instead of offering verbs that cannot work.
func appActionDefs(app domain.App) []actionItem {
	rel, hasActive := app.ActiveRelease()
	if !hasActive {
		return nil
	}
	defs := []actionItem{
		{actAppStatus, "Live status",
			"is " + rel.Container + " running right now", "s", "plain"},
		{actAppLogs, "Logs",
			"the active release's last 100 lines", "l", "plain"},
		{actAppRestart, "Restart",
			"docker restart on the active release", "r", "warn"},
	}
	if _, hasTarget := app.RollbackTarget(); hasTarget {
		defs = append(defs, actionItem{actAppRollback, "Rollback",
			"make the previous release live — typed name confirms", "b", "warn"})
	}
	return append(defs,
		actionItem{actAppShell, "Shell",
			"a real shell inside " + rel.Container, "e", "plain"})
}

// appLiveState is the last live probe's answer about the active
// release — the page's one line of "right now".
type appLiveState struct {
	Probed    bool
	Running   bool
	Container string
	Note      string
}

// appStatusDoneMsg carries the live probe's answer.
type appStatusDoneMsg struct {
	node  string
	app   string
	live  appLiveState
	fatal string
}

// appLogsDoneMsg carries one fetched log tail.
type appLogsDoneMsg struct {
	node  string
	app   string
	lines []string
	fatal string
}

// appRestartDoneMsg carries the restart's outcome.
type appRestartDoneMsg struct {
	node      string
	app       string
	container string
	fatal     string
	note      string
}

// appOpsTimeout bounds one application operation.
const appOpsTimeout = 30 * time.Second

// appDial opens the transport for an application operation; the
// injected runner lets tests drive the verbs hermetically.
func (m Model) appDial(ctx context.Context, node domain.Node) (runner interface {
	Run(context.Context, string, ...string) (string, int, error)
}, closeFn func() error, err error) {
	if m.appRunner != nil {
		return m.appRunner, func() error { return nil }, nil
	}
	client := ssh.New(node, filepath.Join(m.store.Dir(), "known_hosts.json"))
	if err := client.Dial(ctx); err != nil {
		return nil, nil, err
	}
	return client, client.Close, nil
}

// appNeedsRoot asks the node who mymo connected as — sudo -n
// carries the privileged verbs when it is not root, the same rule
// as everywhere else.
func appNeedsRoot(ctx context.Context, r interface {
	Run(context.Context, string, ...string) (string, int, error)
}) bool {
	out, _, err := r.Run(ctx, "id", "-u")
	return err == nil && strings.TrimSpace(out) != "0"
}

// appRunW runs one command through the privilege decision.
func appRunW(ctx context.Context, r interface {
	Run(context.Context, string, ...string) (string, int, error)
}, sudo bool, argv ...string) (string, int, error) {
	if sudo {
		argv = append([]string{"sudo", "-n"}, argv...)
	}
	return r.Run(ctx, argv[0], argv[1:]...)
}

// runAppStatus asks the node what the active container is doing
// right now — status is a question about reality, not the record.
func (m Model) runAppStatus(app domain.App, node domain.Node) tea.Cmd {
	name := app.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), appOpsTimeout)
		defer cancel()
		r, closeFn, err := m.appDial(ctx, node)
		if err != nil {
			return appStatusDoneMsg{node: node.Name, app: name, fatal: err.Error()}
		}
		defer closeFn()
		rel, _ := app.ActiveRelease()
		ops := app.Ops(rel)
		sudo := appNeedsRoot(ctx, r)
		out, code, err := appRunW(ctx, r, sudo, ops.Status...)
		live := appLiveState{Container: rel.Container}
		switch {
		case err != nil:
			live.Note = "the node did not answer (" + err.Error() + ")"
		case code != 0:
			live.Note = "docker refused"
		case lineSays(out, ops.StatusAnswer):
			live.Probed, live.Running = true, true
		default:
			live.Probed, live.Running = true, false
		}
		return appStatusDoneMsg{node: node.Name, app: name, live: live}
	}
}

// runAppLogs fetches the active release's last 100 lines — the
// record says what is live; the container says what happened.
func (m Model) runAppLogs(app domain.App, node domain.Node) tea.Cmd {
	name := app.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), appOpsTimeout)
		defer cancel()
		r, closeFn, err := m.appDial(ctx, node)
		if err != nil {
			return appLogsDoneMsg{node: node.Name, app: name, fatal: err.Error()}
		}
		defer closeFn()
		rel, _ := app.ActiveRelease()
		ops := app.Ops(rel)
		sudo := appNeedsRoot(ctx, r)
		out, code, err := appRunW(ctx, r, sudo, ops.Logs...)
		if err != nil || code != 0 {
			return appLogsDoneMsg{node: node.Name, app: name, fatal: fmt.Sprintf("the node refused (exit %d)", code)}
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = []string{"(the container has said nothing yet)"}
		}
		return appLogsDoneMsg{node: node.Name, app: name, lines: lines}
	}
}

// runAppRestart runs docker restart on the active release. The
// verb is the intent — rollbacks and deploys carry the protections;
// a restart is reversible, so it does not ask for a typed name.
func (m Model) runAppRestart(app domain.App, node domain.Node) tea.Cmd {
	name := app.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), appOpsTimeout)
		defer cancel()
		r, closeFn, err := m.appDial(ctx, node)
		if err != nil {
			return appRestartDoneMsg{node: node.Name, app: name, fatal: err.Error()}
		}
		defer closeFn()
		rel, _ := app.ActiveRelease()
		ops := app.Ops(rel)
		sudo := appNeedsRoot(ctx, r)
		out, code, err := appRunW(ctx, r, sudo, ops.Restart...)
		if err != nil || code != 0 {
			note := strings.TrimSpace(out)
			if i := strings.IndexByte(note, '\n'); i > 0 {
				note = note[:i]
			}
			return appRestartDoneMsg{node: node.Name, app: name, container: rel.Container,
				fatal: fmt.Sprintf("the node refused (exit %d)", code), note: note}
		}
		return appRestartDoneMsg{node: node.Name, app: name, container: rel.Container}
	}
}

// runAppShell hands the operator's terminal to docker exec through
// ssh — a real shell, not an emulation.
func (m Model) runAppShell(app domain.App, node domain.Node) (tea.Model, tea.Cmd) {
	rel, hasActive := app.ActiveRelease()
	if !hasActive {
		return m, m.notify(app.Name+" has no active release to shell into", toastErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), appOpsTimeout)
	defer cancel()
	r, closeFn, err := m.appDial(ctx, node)
	if err != nil {
		return m, m.notify("app shell: "+shorten(err.Error(), 44), toastErr)
	}
	sudo := appNeedsRoot(ctx, r)
	closeFn()
	ops := app.Ops(rel)
	cmd, err := ssh.ExecCommand(node, ssh.InteractiveKnownHostsPath(m.store.Dir()), sudo,
		append([]string{ops.Shell[0]}, ops.Shell[1:]...)...)
	if err != nil {
		return m, m.notify("app shell: "+shorten(err.Error(), 44), toastErr)
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return sshFinishedMsg{err: err} })
}

// bootstrapWord names a bootstrap state in plain words — the
// refusal must never say "is " with nothing after it.
func bootstrapWord(st domain.BootstrapState) string {
	if st == "" {
		return "unverified"
	}
	return strings.ToLower(string(st))
}

// findScreenApp resolves the app the screen names, from the loaded
// record.
func (m Model) findScreenApp(s screen) (domain.App, domain.Node, bool) {
	for i := range m.apps {
		if m.apps[i].Name == s.app && m.apps[i].Node == s.node {
			app := m.apps[i]
			node, err := m.store.GetNode(app.Node)
			if err != nil {
				return domain.App{}, domain.Node{}, false
			}
			return app, node, true
		}
	}
	return domain.App{}, domain.Node{}, false
}

// appDetailView renders one application's whole truth: the record —
// type, exposure, health, every release — the live probe's one line
// of "right now", and the verbs below. When the floor cannot hold
// every row the record walks with j/k, whole rows only; the verbs
// stay pinned, because the page's hands must never scroll away.
func (m Model) appDetailView(s screen) string {
	app, _, ok := m.findScreenApp(s)
	if !ok {
		return m.infoView("Application",
			[]string{s.app + " is not in the record — it may have been removed."},
			"[esc] back")
	}

	artifact := app.Image
	if app.Type == domain.SourceDockerfile {
		artifact = "Dockerfile in " + app.Dockerfile
	}
	expose := "internal only · " + app.Name + ":" + fmt.Sprint(app.Port) + " · no public address"
	exposeStyle := subtextStyle
	if app.Domain != "" {
		expose = "https://" + app.Domain + " · port " + fmt.Sprint(app.Port)
		exposeStyle = textStyle
	}
	health := "not declared — a running container is all mymo can vouch for"
	if app.Health != "" {
		health = app.Health
	}

	w := m.stageW
	head := "\n" + titleStyle.Render(app.Name) + faintStyle.Render(" · "+app.Node) + "\n\n"

	facts := []string{
		factsLabelStyle.Width(10).Render("type") + textStyle.Render(string(app.Type)+" · "+artifact),
		factsLabelStyle.Width(10).Render("exposure") + exposeStyle.Render(expose),
		factsLabelStyle.Width(10).Render("health") + subtextStyle.Render(shorten(health, w-12)),
	}
	if m.appLive.Probed {
		word, style := "NOT running", errStyle
		note := m.appLive.Note
		if m.appLive.Running {
			word, style = "running right now", okStyle
		}
		row := factsLabelStyle.Width(10).Render("live") + style.Render(m.appLive.Container+" · "+word)
		if !m.appLive.Running && note != "" {
			row += " " + subtextStyle.Render(shorten(note, w-34))
		}
		facts = append(facts, row)
	}

	// the verbs live in the footer and the keyboard card; the
	// record answers all the space the page has. A record that
	// cannot fit folds its oldest rows — the fold says so, and the
	// active release never folds
	if len(appActionDefs(app)) == 0 {
		budget := m.contentHeight - lipgloss.Height(head) - len(facts) - 2
		if budget < 1 {
			budget = 1
		}
		inner := head + strings.Join(facts, "\n") + "\n\n" +
			strings.Join(appReleaseRows(app, w-12, budget), "\n") + "\n" +
			faintStyle.Render("nothing to operate — this app has no active release")
		return fitHeight(padLines(inner, w), m.contentHeight)
	}
	budget := m.contentHeight - lipgloss.Height(head) - len(facts) - 2
	if budget < 1 {
		budget = 1
	}
	releases := appReleaseRows(app, w-12, budget)

	inner := head
	inner += strings.Join(facts, "\n") + "\n\n"
	inner += strings.Join(releases, "\n")
	return fitHeight(padLines(inner, w), m.contentHeight)
}

// appReleaseRows renders the releases the page can hold, newest
// first with the active one pinned; rows that do not fit fold
// behind a line that says exactly how many and where the whole
// truth lives. The fold is stated, never silent.
func appReleaseRows(app domain.App, w, budget int) []string {
	if len(app.Releases) == 0 {
		return []string{faintStyle.Render("no releases yet — nothing has been deployed")}
	}
	ordered := make([]domain.Release, 0, len(app.Releases))
	if active, ok := app.ActiveRelease(); ok {
		ordered = append(ordered, active)
	}
	for i := len(app.Releases) - 1; i >= 0; i-- {
		if app.Releases[i].State == domain.ReleaseActive {
			continue
		}
		ordered = append(ordered, app.Releases[i])
	}
	rows := make([]string, 0, len(ordered))
	for _, r := range ordered {
		if len(rows) == budget {
			folded := len(ordered) - budget
			return append(rows, faintStyle.Render(fmt.Sprintf(
				"… %d earlier %s folded — mymo app status %s lists every one",
				folded, plural("release", folded), app.Name)))
		}
		rows = append(rows, appReleaseRow(r, w))
	}
	return rows
}

// plural keeps the fold line honest about counting.
func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// appReleaseRow renders one release row: state, digest, health, age.
func appReleaseRow(r domain.Release, w int) string {
	state, stateStyle := "active", okStyle
	switch r.State {
	case domain.ReleaseRetired:
		state, stateStyle = "kept", subtextStyle
	case domain.ReleaseFailed:
		state, stateStyle = "failed", errStyle
	}
	digest := shortAppDigest(r.Digest)
	age := "unknown when"
	if !r.DeployedAt.IsZero() {
		age = facts.FormatAge(r.DeployedAt)
	}
	line := stateStyle.Width(7).Render(state) + dimStyle.Width(17).Render(digest)
	if r.Health != "" {
		if r.State == domain.ReleaseFailed {
			line += subtextStyle.Render(shorten(r.Health, w-24))
		} else {
			line += subtextStyle.Render(shorten(r.Health, 14))
		}
	}
	return factsLabelStyle.Width(10).Render("r"+fmt.Sprint(r.ID)) + line + " " + faintStyle.Render(age)
}

// appLogsView renders the fetched tail in a walking window: whole
// lines only, the title and the way out pinned.
func (m Model) appLogsView(s screen) string {
	w := m.stageW
	head := titleStyle.Render("Logs") + " " +
		faintStyle.Render("· "+s.app+" · "+s.node+" · the last 100 lines") + "\n\n"

	max := m.logsScrollMax(len(m.appLogs))
	tail := "\n" + faintStyle.Render("esc back") + "\n"
	if max > 0 {
		tail += faintStyle.Render("j/k scroll") + "\n"
	}

	inner := head
	window := m.logsWindowRows()
	if max > 0 {
		window-- // the hint row is paid for by the window
	}
	taken := 0
	var b strings.Builder
	for i := m.logsScroll; i < len(m.appLogs) && taken < window; i++ {
		if taken > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.logLineStyle(i, w))
		taken++
	}
	if taken > 0 {
		inner += b.String() + "\n"
	}
	inner += tail
	return fitHeight(padLines(inner, w), m.contentHeight)
}

// logLineStyle renders one log line: timestamps and levels keep
// their plainness; the row is clipped to the stage, never wrapped
// into a lie about what the container said.
func (m Model) logLineStyle(i, w int) string {
	line := m.appLogs[i]
	if strings.HasPrefix(strings.TrimSpace(line), "(") {
		return faintStyle.Render(line)
	}
	return subtextStyle.Render(line)
}

// logsScrollMax is the last line the window can start from, from
// the tail because lines never wrap.
func (m Model) logsScrollMax(rowCount int) int {
	last := rowCount - m.logsWindowRows() + 1
	if last < 0 {
		return 0
	}
	return last
}

// utf8Len counts runes, not bytes.
func utf8Len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// openApp enters an application's detail page: the record above,
// the verbs below. Entering resets the page's one line of "right
// now" — a live answer belongs to the visit that asked for it.
func (m *Model) openApp(a domain.App) {
	m.appLive = appLiveState{}
	m.appLogs = nil
	m.logsScroll = 0
	m.push(screen{kind: scAppDetail, node: a.Node, app: a.Name})
}

// updateAppDetailKeys runs one verb per key — the same verbs the
// keyboard card names; while an operation runs, the ceremony owns
// the keys.
func (m Model) updateAppDetailKeys(s screen, str string) (tea.Model, tea.Cmd) {
	if m.loading.active {
		switch str {
		case "q":
			return m, tea.Quit
		case "esc":
			m.cancelLoading()
			return m, nil
		}
		return m, nil
	}
	app, node, ok := m.findScreenApp(s)
	if !ok && str != "esc" && str != "?" {
		return m, m.notify(s.app+" is not in the record — it may have been removed", toastErr)
	}
	run := func(id actionID) (tea.Model, tea.Cmd) {
		if !ok {
			return m, nil
		}
		return m.runAppAction(actionItem{id: id}, app, node)
	}
	switch str {
	case "s":
		return run(actAppStatus)
	case "l":
		return run(actAppLogs)
	case "r":
		return run(actAppRestart)
	case "b":
		if ok {
			if _, hasTarget := app.RollbackTarget(); hasTarget {
				return run(actAppRollback)
			}
		}
		return m, nil
	case "e":
		return run(actAppShell)
	case "esc":
		m.pop()
		return m, nil
	}
	return m.updateSimpleKeys(s.kind, str)
}

// runAppAction runs one application verb — the same commands the
// CLI runs, over the same transport.
func (m Model) runAppAction(a actionItem, app domain.App, node domain.Node) (tea.Model, tea.Cmd) {
	switch a.id {
	case actAppStatus:
		m.loading = loadingState{
			active: true,
			line:   "asking " + node.Name + " about " + app.Name,
			detail: "one read-only probe — the container's status, nothing else",
			start:  time.Now(),
		}
		m.loadingApp = app.Name
		return m, tea.Batch(m.spinner.Tick, m.runAppStatus(app, node))
	case actAppLogs:
		m.loading = loadingState{
			active: true,
			line:   "reading " + app.Name + "'s logs on " + node.Name,
			detail: "the active release's last 100 lines",
			start:  time.Now(),
		}
		m.loadingApp = app.Name
		return m, tea.Batch(m.spinner.Tick, m.runAppLogs(app, node))
	case actAppRestart:
		if node.Bootstrap.State != domain.BootstrapReady {
			return m, m.notify(node.Name+" is "+bootstrapWord(node.Bootstrap.State)+
				" — mymo only operates on a node it has verified", toastErr)
		}
		m.loading = loadingState{
			active: true,
			line:   "restarting " + app.Name + " on " + node.Name,
			detail: "docker restart on the active release",
			start:  time.Now(),
		}
		m.loadingApp = app.Name
		return m, tea.Batch(m.spinner.Tick, m.runAppRestart(app, node))
	case actAppRollback:
		if node.Bootstrap.State != domain.BootstrapReady {
			return m, m.notify(node.Name+" is "+bootstrapWord(node.Bootstrap.State)+
				" — mymo only operates on a node it has verified", toastErr)
		}
		m.rbTyped = ""
		m.push(screen{kind: scAppRollback, node: node.Name, app: app.Name})
		return m, nil
	case actAppShell:
		if node.Bootstrap.State != domain.BootstrapReady {
			return m, m.notify(node.Name+" is "+bootstrapWord(node.Bootstrap.State)+
				" — mymo only operates on a node it has verified", toastErr)
		}
		return m.runAppShell(app, node)
	}
	return m, nil
}

// handleAppStatusDone lands the live probe's answer on the page.
func (m Model) handleAppStatusDone(msg appStatusDoneMsg) (tea.Model, tea.Cmd) {
	if m.loading.active && m.loadingApp == msg.app {
		m.loading = loadingState{}
	}
	if m.cur().kind == scAppDetail && m.cur().app == msg.app {
		if msg.fatal != "" {
			return m, m.notify(shorten(msg.fatal, 44), toastErr)
		}
		m.appLive = msg.live
	}
	return m, nil
}

// handleAppLogsDone opens the tail in its own page.
func (m Model) handleAppLogsDone(msg appLogsDoneMsg) (tea.Model, tea.Cmd) {
	if m.loading.active && m.loadingApp == msg.app {
		m.loading = loadingState{}
	}
	if msg.fatal != "" {
		return m, m.notify(shorten(msg.fatal, 44), toastErr)
	}
	m.appLogs = msg.lines
	m.logsScroll = m.logsScrollMax(len(msg.lines))
	m.push(screen{kind: scAppLogs, node: msg.node, app: msg.app})
	return m, nil
}

// handleAppRestartDone lands the restart's outcome.
func (m Model) handleAppRestartDone(msg appRestartDoneMsg) (tea.Model, tea.Cmd) {
	if m.loading.active && m.loadingApp == msg.app {
		m.loading = loadingState{}
	}
	if msg.fatal != "" {
		note := msg.note
		if note != "" {
			note = ": " + note
		}
		return m, m.notify(shorten("restart failed "+note, 44), toastErr)
	}
	return m, m.notify(msg.container+" restarted", toastOK)
}

// updateAppLogsKeys walks the tail's window: whole lines only.
func (m Model) updateAppLogsKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "j", "down":
		if m.logsScroll < m.logsScrollMax(len(m.appLogs)) {
			m.logsScroll++
		}
		return m, nil
	case "k", "up":
		if m.logsScroll > 0 {
			m.logsScroll--
		}
		return m, nil
	case "esc":
		m.pop()
		return m, nil
	}
	return m.updateSimpleKeys(m.cur().kind, str)
}

// logsHeadHeight is the fixed rows above the window: the title
// and its air.
const logsHeadHeight = 2

// logsWindowRows is the lines the tail's window holds: the page
// minus the head, the esc row, and its air.
func (m Model) logsWindowRows() int {
	n := m.contentHeight - logsHeadHeight - 2
	if n < 1 {
		return 1
	}
	return n
}

// appRollbackDoneMsg carries the engine's whole result — persisted
// by the handler on success AND failure, because a failed rollback
// is part of the history too.
type appRollbackDoneMsg struct {
	node  string
	app   string
	res   deploy.Result
	fatal string
}

// appProgressMsg carries one ceremony line while a rollback runs.
type appProgressMsg struct {
	line string
}

// updateAppRollbackKeys is the typed-name gate, the same ceremony
// as a deploy: runes build the name, enter checks it in full, esc
// aborts without a trace.
func (m Model) updateAppRollbackKeys(s screen, str string) (tea.Model, tea.Cmd) {
	if m.loading.active {
		switch str {
		case "q":
			return m, tea.Quit
		case "esc":
			m.cancelLoading()
			return m, nil
		}
		return m, nil
	}
	app, _, ok := m.findScreenApp(s)
	if !ok {
		m.pop()
		return m, m.notify(s.app+" is not in the record — it may have been removed", toastErr)
	}
	switch str {
	case "esc":
		m.pop()
		return m, nil
	case "enter":
		if m.rbTyped != app.Name {
			m.rbTyped = ""
			return m, m.notify("that does not match "+app.Name+" — nothing runs until it does", toastWarn)
		}
		target, hasTarget := app.RollbackTarget()
		if !hasTarget {
			m.pop()
			return m, m.notify(app.Name+" has no retired release to roll back to", toastWarn)
		}
		_, node, ok := m.findScreenApp(s)
		if !ok {
			m.pop()
			return m, m.notify(s.app+" is not in the record — it may have been removed", toastErr)
		}
		return m.runAppRollback(app, node, target)
	case "backspace":
		if n := utf8.RuneCountInString(m.rbTyped); n > 0 {
			m.rbTyped = string([]rune(m.rbTyped)[:n-1])
		}
		return m, nil
	default:
		if utf8.RuneCountInString(str) == 1 {
			m.rbTyped += str
		}
		return m, nil
	}
}

// runAppRollback runs the engine behind the loading ceremony; the
// progress pump keeps the page narrating what the box is doing.
func (m Model) runAppRollback(app domain.App, node domain.Node, target domain.Release) (tea.Model, tea.Cmd) {
	m.loading = loadingState{
		active: true,
		line:   "rolling " + app.Name + " back to release " + fmt.Sprint(target.ID),
		detail: "starting the kept release, switching caddy to it",
		start:  time.Now(),
	}
	m.loadingApp = app.Name
	name := app.Name
	progress := make(chan string, 32)
	m.appProgress = progress

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r, closeFn, err := m.appDial(ctx, node)
		if err != nil {
			close(progress)
			return appRollbackDoneMsg{node: node.Name, app: name, fatal: err.Error()}
		}
		defer closeFn()
		sudo := appNeedsRoot(ctx, r)
		res := deploy.Rollback(ctx, r, app, deploy.Options{
			Sudo:     sudo,
			Progress: func(line string) { progress <- line },
		})
		close(progress)
		return appRollbackDoneMsg{node: node.Name, app: name, res: res}
	}, m.appProgressReader())
}

// appProgressReader is the rollback pump's read end: one line per
// message, re-issued until the channel closes.
func (m Model) appProgressReader() tea.Cmd {
	ch := m.appProgress
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return appDrainedMsg{}
		}
		return appProgressMsg{line: line}
	}
}

// appDrainedMsg marks the pump closed.
type appDrainedMsg struct{}

// handleAppProgress lands one ceremony line in the loading detail.
func (m Model) handleAppProgress(msg appProgressMsg) (tea.Model, tea.Cmd) {
	if m.loading.active && msg.line != "" {
		m.loading.detail = msg.line
	}
	return m, m.appProgressReader()
}

// handleAppRollbackDone lands the engine's result: the record is
// written on success AND failure, the page states which release
// serves now.
func (m Model) handleAppRollbackDone(msg appRollbackDoneMsg) (tea.Model, tea.Cmd) {
	if m.loading.active && m.loadingApp == msg.app {
		m.loading = loadingState{}
	}
	m.appProgress = nil
	if msg.fatal != "" {
		if m.cur().kind == scAppRollback {
			m.pop()
		}
		return m, m.notify(shorten(msg.fatal, 44), toastErr)
	}
	if err := m.store.UpdateApp(msg.res.App); err != nil {
		if m.cur().kind == scAppRollback {
			m.pop()
		}
		return m, m.notify("recording the rollback: "+shorten(err.Error(), 36), toastErr)
	}
	m.reloadApps()
	rel, _ := msg.res.App.ActiveRelease()
	if m.cur().kind == scAppRollback {
		m.pop()
	}
	if msg.res.OK {
		return m, m.notify(msg.res.App.Name+" release "+fmt.Sprint(rel.ID)+" is live again", toastOK)
	}
	note := "release " + fmt.Sprint(rel.ID) + " keeps serving"
	if msg.res.FailedAt != "" {
		note = shorten(msg.res.FailedAt, 30) + " — " + note
	}
	return m, m.notify("rollback failed: "+note, toastErr)
}

// rollbackView renders the typed-name gate: what is live, what is
// kept, and the name typed in full as the confirmation.
func (m Model) rollbackView(s screen) string {
	app, _, ok := m.findScreenApp(s)
	if !ok {
		return m.infoView("Rollback",
			[]string{s.app + " is not in the record — it may have been removed."},
			"[esc] back")
	}
	target, hasTarget := app.RollbackTarget()
	if !hasTarget {
		return m.infoView("Rollback",
			[]string{app.Name + " has no retired release to roll back to."},
			"[esc] back")
	}
	current, _ := app.ActiveRelease()

	inner := titleStyle.Render("Rollback") + " " +
		faintStyle.Render("· "+app.Name+" · "+app.Node) + "\n\n" +
		factsLabelStyle.Width(10).Render("live") + textStyle.Render("release "+fmt.Sprint(current.ID)+" · "+current.Container) + "\n" +
		factsLabelStyle.Width(10).Render("kept") + subtextStyle.Render("release "+fmt.Sprint(target.ID)+" · "+target.Container+" · "+shortAppDigest(target.Digest)) + "\n" +
		factsLabelStyle.Width(10).Render("since") + faintStyle.Render(facts.FormatAge(target.DeployedAt)) + "\n\n" +
		subtextStyle.Render("no rebuild — the kept release starts as it was, caddy switches to it") + "\n\n"

	typed := m.rbTyped
	cursor := accentStyle.Render("\u258c")
	if typed == app.Name {
		cursor = okStyle.Render("\u258c")
	}
	inner += subtextStyle.Render("type ") + accentStyle.Render(app.Name) +
		subtextStyle.Render(" in full to roll back \u2014 anything else aborts") + "\n" +
		accentStyle.Render("> ") + textStyle.Render(typed) + cursor

	return m.wizardPanelFit(padLines(inner, m.stageW))
}

// lineSays reports whether the command's output carries the
// expected answer as one of its lines.
func lineSays(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
