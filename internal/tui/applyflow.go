package tui

import (
	"context"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/apply"
	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// Real installs take real time: the apply's budget is minutes, not
// the probe's seconds.
const (
	applyTimeout  = 10 * time.Minute
	verifyTimeout = 2 * time.Minute
)

// applyProgressMsg carries one progress line from the engine while
// the apply runs. applyDrainedMsg ends the pump once the channel
// closes.
type applyProgressMsg struct {
	node string
	line string
}

type applyDrainedMsg struct{}

// applyDoneMsg reports the engine's result — or why it never ran.
type applyDoneMsg struct {
	node string
	res  apply.Result
	err  error
}

// verifyDoneMsg reports the effective-state verification.
type verifyDoneMsg struct {
	node   string
	checks []preflight.Check
	ok     bool
}

// updatePlanKeys is the plan page's keyboard: the typed name is the
// confirmation — runes build it, enter checks it in full, esc goes
// back without a trace.
func (m Model) updatePlanKeys(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "esc":
		m.pop()
		return m, nil
	case "enter":
		if len(m.planSteps) == 0 {
			return m, m.notify("no plan to apply — draft one from a cleared check", toastWarn)
		}
		if m.applyTyped != m.selNode.Name {
			// a refused name does not linger to corrupt the next
			// attempt — the buffer starts over with the operator
			m.applyTyped = ""
			return m, m.notify("that does not match "+m.selNode.Name+" — nothing runs until it does", toastWarn)
		}
		return m.runApply()
	case "backspace":
		if n := utf8.RuneCountInString(m.applyTyped); n > 0 {
			m.applyTyped = string([]rune(m.applyTyped)[:n-1])
		}
		return m, nil
	default:
		if utf8.RuneCountInString(str) == 1 {
			m.applyTyped += str
		}
		return m, nil
	}
}

// runApply executes the confirmed plan over the borrowed connection:
// the states advance (confirmed, then applying), the ceremony carries
// the engine's live progress through a channel pump, and the report
// screen holds the outcome.
func (m Model) runApply() (tea.Model, tea.Cmd) {
	n := m.selNode
	// the plan is no longer under review — it is running
	m.pop()
	m.push(screen{kind: scNodeApplyReport, node: n.Name})
	m.applyResult = apply.Result{}
	m.applyChecks = nil

	m.loading = loadingState{
		active: true,
		line:   "applying " + n.Name,
		detail: "the engine runs the plan step by step",
		start:  time.Now(),
	}
	m.layout()

	client := m.liveClient
	if client != nil {
		m.pauseLive()
	}
	m.auditBusy = true
	m.advanceBootstrap(n.Name, domain.BootstrapConfirmed, "confirmed")
	m.advanceBootstrap(n.Name, domain.BootstrapApplying, "applying")

	name := n.Name
	steps := m.planSteps
	store := m.store
	sudo := m.pfSnap.User != "root"
	node := n
	runner := m.applyRunner
	proverFn := m.newProver
	progress := make(chan string, 64)
	m.applyProgress = progress
	m.applyOpts = apply.Options{Sudo: sudo}

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
		defer cancel()

		pub, err := ssh.GenerateEd25519(mymoTUIKeyPath(store, name))
		if err != nil {
			return applyDoneMsg{node: name, err: err}
		}
		prover := proverFn
		if prover == nil {
			prover = func(string) apply.Prover {
				return apply.SSHProver{Node: node, PrivateKey: mymoTUIKeyPath(store, name), KnownHosts: filepath.Join(store.Dir(), "known_hosts.json")}
			}
		}
		opts := apply.Options{
			Sudo:          sudo,
			MymoPublicKey: pub,
			Now:           time.Now(),
			Prover:        prover(mymoTUIKeyPath(store, name)),
			Progress:      func(line string) { progress <- line },
		}

		if runner != nil {
			res := apply.Apply(ctx, runner, steps, opts)
			close(progress)
			return applyDoneMsg{node: name, res: res}
		}
		if client != nil {
			res := apply.Apply(ctx, client, steps, opts)
			close(progress)
			return applyDoneMsg{node: name, res: res}
		}
		c := ssh.New(node, filepath.Join(store.Dir(), "known_hosts.json"))
		c.WithProgress(m.netSink())
		if err := c.Dial(ctx); err != nil {
			close(progress)
			return applyDoneMsg{node: name, err: err}
		}
		defer c.Close()
		res := apply.Apply(ctx, c, steps, opts)
		close(progress)
		return applyDoneMsg{node: name, res: res}
	}, m.applyProgressReader(), m.netProgressReader())
}

// applyProgressReader is the pump's read end: one line per message,
// re-issued by the progress handler until the channel closes.
func (m Model) applyProgressReader() tea.Cmd {
	ch := m.applyProgress
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return applyDrainedMsg{}
		}
		return applyProgressMsg{node: m.cur().node, line: line}
	}
}

// handleApplyDone lands the engine's result. A failure keeps the
// report on screen — where it stopped and why deserves the whole
// view, not a toast.
func (m Model) handleApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	m.applyResult = msg.res

	if msg.err != nil {
		m.auditBusy = false
		m.loading = loadingState{}
		m.layout()
		if m.cur().kind == scNodeApplyReport {
			m.pop()
		}
		return m, m.notify("apply failed: "+shorten(msg.err.Error(), 44), toastErr)
	}

	if msg.res.Failed {
		m.auditBusy = false
		m.loading = loadingState{}
		m.layout()
		m.reopenBootstrap(msg.node, "apply stopped at "+msg.res.FailedAt+" — preflight again to resume")
		if m.cur().kind != scNodeApplyReport {
			return m, m.notify("the apply stopped at "+msg.res.FailedAt+" — walk back to see why", toastErr)
		}
		return m, m.notify("the apply stopped — the report says where", toastErr)
	}

	// success: verify the effective state over the same connection
	if m.cur().kind == scNodeApplyReport {
		m.loading = loadingState{
			active: true,
			line:   "verifying " + msg.node,
			detail: "effective state, not config files",
			start:  time.Now(),
		}
		m.layout()
	}
	return m, m.runVerify(msg.node)
}

// runVerify proves the baseline is live: sshd's real posture, the
// firewall, the daemon, the marker — and the mymo key proven again
// on a second connection against the reloaded sshd.
func (m Model) runVerify(node string) tea.Cmd {
	opts := m.applyOpts
	runner := m.applyRunner
	client := m.liveClient
	store := m.store
	n := m.selNode
	if m.selNode.Name != node {
		if store != nil {
			if nn, err := store.GetNode(node); err == nil {
				n = nn
			}
		}
	}
	knownHosts := ""
	if store != nil {
		knownHosts = filepath.Join(store.Dir(), "known_hosts.json")
	}
	proverFn := m.newProver
	if proverFn == nil {
		proverFn = func(string) apply.Prover {
			return apply.SSHProver{Node: n, PrivateKey: mymoTUIKeyPath(store, node), KnownHosts: knownHosts}
		}
	}
	if opts.Prover == nil {
		opts.Prover = proverFn(mymoTUIKeyPath(store, node))
	}
	if opts.MymoPublicKey == "" {
		// a walk-away verify (or a test) without apply's key: read
		// the pair mymo already keeps for this node
		if pub, err := ssh.GenerateEd25519(mymoTUIKeyPath(store, node)); err == nil {
			opts.MymoPublicKey = pub
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
		defer cancel()
		r := preflight.Runner(client)
		if runner != nil {
			r = runner
		} else if client == nil {
			c := ssh.New(n, knownHosts)
			if err := c.Dial(ctx); err != nil {
				return verifyDoneMsg{node: node, checks: nil, ok: false}
			}
			defer c.Close()
			r = c
		}
		checks, ok := apply.Verify(ctx, r, opts, baseline.Version)
		return verifyDoneMsg{node: node, checks: checks, ok: ok}
	}
}

// handleVerifyDone records the lifecycle's honest position: READY
// only when every effective check passed.
func (m Model) handleVerifyDone(msg verifyDoneMsg) (tea.Model, tea.Cmd) {
	m.auditBusy = false
	m.loading = loadingState{}
	m.layout()
	m.applyChecks = msg.checks
	if !msg.ok {
		m.reopenBootstrap(msg.node, "verify failed — preflight again to resume")
		return m, m.notify("the baseline ran but did not verify — mymo will not call this node ready", toastErr)
	}
	m.advanceBootstrap(msg.node, domain.BootstrapReady, "ready")
	return m, m.notify("mymo security baseline applied and verified", toastOK)
}

// advanceBootstrap moves the node's lifecycle record forward — never
// backward — and refreshes what the fleet knows.
func (m *Model) advanceBootstrap(name string, to domain.BootstrapState, verdict string) {
	if m.store == nil {
		return
	}
	n, err := m.store.GetNode(name)
	if err != nil {
		return
	}
	if domain.BootstrapRank(to) < domain.BootstrapRank(n.Bootstrap.State) {
		return
	}
	n.Bootstrap = domain.Bootstrap{State: to, Baseline: baseline.Version, Verdict: verdict, At: time.Now()}
	if err := m.store.UpdateNode(n); err != nil {
		return
	}
	m.reloadFleet()
	if m.selNode.Name == name {
		m.selNode = n
	}
}

// reopenBootstrap rolls a failed run back to the start of the flow.
// The lifecycle never steps back on success; failure is the one
// honest exception: an apply that stopped must be re-audited, because
// the box is whatever it is now — and preflight, not the plan, is
// what reads it. Without this, a failed apply would strand the
// record at "applying" with no way back in.
func (m *Model) reopenBootstrap(name, verdict string) {
	if m.store == nil {
		return
	}
	n, err := m.store.GetNode(name)
	if err != nil {
		return
	}
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapPreflight, Verdict: verdict, At: time.Now()}
	if err := m.store.UpdateNode(n); err != nil {
		return
	}
	m.reloadFleet()
	if m.selNode.Name == name {
		m.selNode = n
	}
}

// setBootstrapVerdict records what happened without moving the
// state.
func (m *Model) setBootstrapVerdict(name, verdict string) {
	if m.store == nil {
		return
	}
	n, err := m.store.GetNode(name)
	if err != nil {
		return
	}
	n.Bootstrap.Verdict = verdict
	n.Bootstrap.At = time.Now()
	if err := m.store.UpdateNode(n); err != nil {
		return
	}
	m.reloadFleet()
	if m.selNode.Name == name {
		m.selNode = n
	}
}

// mymoTUIKeyPath is the mymo key pair's home for one node.
func mymoTUIKeyPath(store interface{ Dir() string }, name string) string {
	return filepath.Join(store.Dir(), "keys", name+".key")
}

// applyReportView renders the outcome: every step done, kept, failed,
// or blocked — the failure's full reason, the gate's proof, the
// verify rows, and the lifecycle's honest position.
func (m Model) applyReportView() string {
	var b strings.Builder
	w := m.contentWidth - 8 // the card's frame eats 8 columns
	verdict := "applied"
	if m.applyResult.Failed {
		verdict = "stopped at " + m.applyResult.FailedAt
	}
	b.WriteString(titleStyle.Render("Initial setup") + " " +
		faintStyle.Render("\u00b7 the report \u00b7 "+m.selNode.Name+" \u00b7 "+verdict) + "\n\n")

	for _, s := range m.applyResult.Steps {
		// the state word is the row's spine: uppercase, one width,
		// so done and failure read as the same column
		state, style := "DONE", okStyle
		switch s.State {
		case apply.StepDone:
		case apply.StepKept:
			state, style = "KEPT", faintStyle
		case apply.StepFailed:
			state, style = "FAILED", errStyle
		default:
			state, style = "BLOCKED", faintStyle
		}
		b.WriteString("  " + style.Width(7).Render(state) + " " + textStyle.Render(s.Title) + "\n")
		if s.State == apply.StepFailed {
			for _, line := range wrapDetail(s.Note, w-9) {
				b.WriteString(strings.Repeat(" ", 9) + subtextStyle.Render(line) + "\n")
			}
		}
	}
	// the gate's proof is not a step and never was: the loading card
	// narrated it live ("proving the mymo key on a second
	// connection"), and the sshd row below carries its outcome
	if len(m.applyChecks) > 0 {
		b.WriteString("\n")
		for _, c := range m.applyChecks {
			// the mark is padded to one width so every detail starts
			// in the same column; wrapped details continue under it,
			// never as new rows with the prefix repeated
			mark := okStyle.Render("ok     ")
			if c.Outcome != preflight.Pass {
				mark = errStyle.Render("FAILED ")
			}
			for i, line := range wrapDetail(c.Detail, w-29) {
				if i == 0 {
					b.WriteString("  " + mark + " " + factsLabelStyle.Width(18).Render(c.Title) + " " +
						subtextStyle.Render(line) + "\n")
				} else {
					b.WriteString(strings.Repeat(" ", 29) + subtextStyle.Render(line) + "\n")
				}
			}
		}
	}

	b.WriteString("\n")
	switch {
	case m.applyResult.Failed:
		for _, line := range wrapDetail("the steps after the failure were never attempted — the operator's current access is untouched", w) {
			b.WriteString(warnStyle.Render(line) + "\n")
		}
	case len(m.applyChecks) > 0 && m.selNode.Bootstrap.State == domain.BootstrapReady:
		b.WriteString(okStyle.Render("mymo security baseline applied and verified") + "\n")
		b.WriteString(faintStyle.Render("promote to app host when ready: mymo node promote "+m.selNode.Name) + "\n")
	default:
		b.WriteString(faintStyle.Render("esc back — the report stays in the node's lifecycle record") + "\n")
	}
	return m.wizardPanelFit(b.String())
}

// centeredPanel is the dialog pattern: centered on the stage.
func (m Model) centeredPanel(inner string) string {
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, panelStyle.Render(inner))
}

// wrapDetail keeps long notes inside the report's columns.
func wrapDetail(s string, w int) []string {
	if w < 20 {
		w = 20
	}
	words := strings.Fields(s)
	var lines []string
	var cur string
	for _, word := range words {
		if cur == "" {
			cur = word
			continue
		}
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) > w {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
