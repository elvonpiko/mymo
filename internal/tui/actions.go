package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// checkTimeout bounds one whole probe attempt.
const checkTimeout = 90 * time.Second

// checkDoneMsg reports a probe attempt's outcome. On success the
// connection is still open, carried along so the observe page can
// adopt it for live stats — entering a node costs exactly one dial.
type checkDoneMsg struct {
	node   string
	snap   facts.Node
	err    error
	client *ssh.Client // open on success; already closed on failure
	seq    int         // which entry started this probe
}

// sshFinishedMsg reports the interactive session handed to the system
// ssh client ending.
type sshFinishedMsg struct{ err error }

// fleetSetChecking rebuilds the fleet rows, marking the named node
// as being probed; an empty name clears every flag.
func (m *Model) fleetSetChecking(name string) {
	items := make([]list.Item, 0, len(m.nodes))
	for _, n := range m.nodes {
		items = append(items, fleetItem{node: n, checking: n.Name == name})
	}
	m.fleet.SetItems(items)
}

// newCheckSpinner builds the probe spinner: accent-colored braille
// dots animating while a check runs.
func newCheckSpinner() spinner.Model {
	return spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(accentStyle),
	)
}

// beginCheck probes the node in the background. The probe is
// read-only; a failure never touches the node's last good snapshot.
func (m Model) beginCheck(n domain.Node, seq int) tea.Cmd {
	knownHosts := filepath.Join(m.store.Dir(), "known_hosts.json")
	name := n.Name
	return func() tea.Msg {
		client := ssh.New(n, knownHosts)
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if err := client.Dial(ctx); err != nil {
			return checkDoneMsg{node: name, err: err, seq: seq}
		}
		snap, err := facts.Probe(ctx, client)
		if err != nil {
			client.Close()
			return checkDoneMsg{node: name, err: err, seq: seq}
		}
		return checkDoneMsg{node: name, snap: snap, client: client, seq: seq}
	}
}

// handleCheckDone records the probe outcome in the node's record and
// refreshes the fleet: a success stores the snapshot, a failure stores
// why it failed — health never silently reverts to green. When the
// user is still on the node's page, the probe's connection becomes
// the live session; otherwise it is closed and the result toasts.
func (m Model) handleCheckDone(msg checkDoneMsg) (tea.Model, tea.Cmd) {
	// A superseded probe (the user left and entered another node) still
	// records its observation, but must not disturb the running one.
	if msg.seq == m.checkSeq {
		m.loading = loadingState{}
		// geometry follows chrome: back to header-page heights
		m.layout()
	}
	m.fleetSetChecking(m.loading.node)

	closeClient := func() {
		if msg.client != nil {
			msg.client.Close()
		}
	}
	if m.store == nil {
		closeClient()
		return m, nil
	}
	n, err := m.store.GetNode(msg.node)
	if err != nil {
		closeClient()
		return m, m.notify("state error: "+shorten(err.Error(), 40), toastErr)
	}
	if msg.err != nil {
		n.LastCheck = domain.CheckState{At: time.Now(), Error: msg.err.Error()}
	} else {
		n.Facts = msg.snap
		n.LastCheck = domain.CheckState{At: time.Now()}
	}
	if err := m.store.UpdateNode(n); err != nil {
		closeClient()
		return m, m.notify(err.Error(), toastErr)
	}
	m.reloadFleet()

	onPage := m.cur().kind == scNode && m.cur().node == msg.node && !m.loading.active
	if !onPage {
		closeClient()
		if msg.err != nil {
			return m, m.notify("check failed: "+shorten(msg.err.Error(), 44), toastErr)
		}
		return m, m.notify("checked "+msg.node, toastOK)
	}

	// The page is showing: its own state change is the feedback, so no
	// toast. Failure lands in the card; success turns the card live.
	m.selNode = n
	if msg.err != nil {
		m.live = false
		m.liveErr = shorten(msg.err.Error(), 30)
		return m, nil
	}
	m.live = true
	m.livePaused = false
	m.liveSeq++
	m.liveClient = msg.client
	m.liveErr = ""
	// seed the live memory readout from the fresh snapshot so the card
	// never shows a blank while the first sample is in flight
	m.liveCur = liveSample{memAvail: msg.snap.MemAvail, memTotal: msg.snap.MemTotal}
	return m, m.sampleLive(m.liveSeq)
}

// pfDoneMsg reports the app-host preflight for the node the audit
// was started for: a fresh snapshot plus the deep read-only scan, or
// why the audit could not complete.
type pfDoneMsg struct {
	node  string
	snap  facts.Node
	audit preflight.Audit
	err   error
}

// runPreflight starts the app-host audit for the selected node: a
// fresh discovery snapshot plus the deep read-only scan, over the
// live connection when one is open — one dial, one audit. The
// ceremony fronts the wait; leaving cancels the visit, not the audit.
func (m Model) runPreflight() (tea.Model, tea.Cmd) {
	if m.store == nil {
		return m, m.notify("state store unavailable", toastErr)
	}
	n := m.selNode
	m.push(screen{kind: scNodePreflight, node: n.Name})
	m.loading = loadingState{
		active: true,
		line:   "auditing " + n.Name + " over SSH",
		detail: "preflight is read-only · nothing is modified",
		start:  time.Now(),
	}
	m.layout()
	client := m.liveClient
	// the audit borrows the live connection: sampling pauses so a
	// slow tick can never time out and close it mid-audit
	if client != nil {
		m.pauseLive()
	}
	m.auditBusy = true
	knownHosts := filepath.Join(m.store.Dir(), "known_hosts.json")
	name := n.Name
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if client == nil {
			c := ssh.New(n, knownHosts)
			if err := c.Dial(ctx); err != nil {
				return pfDoneMsg{node: name, err: err}
			}
			defer c.Close()
			return probeAndAudit(ctx, c, name)
		}
		return probeAndAudit(ctx, client, name)
	})
}

// probeAndAudit runs the discovery probe and the deep scan in one
// pass over an open connection.
func probeAndAudit(ctx context.Context, r preflight.Runner, name string) tea.Msg {
	snap, err := facts.Probe(ctx, r)
	if err != nil {
		return pfDoneMsg{node: name, err: err}
	}
	audit, err := preflight.Scan(ctx, r)
	if err != nil {
		return pfDoneMsg{node: name, err: err}
	}
	return pfDoneMsg{node: name, snap: snap, audit: audit}
}

// handlePreflightDone records the audit on the node and shows the
// verdicts. A cancelled visit still records: the audit is read-only,
// so its result is always true and always worth keeping.
func (m Model) handlePreflightDone(msg pfDoneMsg) (tea.Model, tea.Cmd) {
	m.auditBusy = false
	m.loading = loadingState{}
	m.layout()
	if msg.err != nil {
		if m.cur().kind == scNodePreflight {
			m.pop()
		}
		return m, m.notify("preflight failed: "+shorten(msg.err.Error(), 44), toastErr)
	}
	checks := preflight.Evaluate(msg.snap, msg.audit)
	verdict := preflight.Verdict(checks)
	m.pfChecks = checks
	m.pfVerdict = verdict
	m.pfSnap = msg.snap
	m.pfAudit = msg.audit

	if m.store != nil {
		if n, err := m.store.GetNode(msg.node); err == nil {
			n.Facts = msg.snap
			n.LastCheck = domain.CheckState{At: time.Now()}
			// recorded while at or before the preflight step; deeper
			// lifecycle positions are never rewound by an audit
			if n.Bootstrap.State == domain.BootstrapNone || n.Bootstrap.State == domain.BootstrapPreflight {
				n.Bootstrap = domain.Bootstrap{
					State:    domain.BootstrapPreflight,
					Baseline: baseline.Version,
					Verdict:  verdict.String(),
					At:       time.Now(),
				}
			}
			if err := m.store.UpdateNode(n); err != nil {
				return m, m.notify(err.Error(), toastErr)
			}
			m.reloadFleet()
			if m.selNode.Name == msg.node {
				m.selNode = n
			}
		}
	}
	if m.cur().kind != scNodePreflight {
		// the user left before the audit landed: toast the verdict
		return m, m.notify("preflight "+msg.node+": "+verdict.String(), toastOK)
	}
	return m, nil
}

// planMsg reports the generated plan for the node the flow started
// for, or why generation refused.
type planMsg struct {
	node  string
	steps []plan.Step
	err   error
}

// runPlan continues a cleared preflight into the plan step: the
// pinned baseline and the audit's findings become the concrete
// change list, under the same ceremony as the audit. The plan flow
// only runs from a cleared verdict; a blocked one toasts instead.
func (m Model) runPlan() (tea.Model, tea.Cmd) {
	if m.store == nil {
		return m, m.notify("state store unavailable", toastErr)
	}
	if m.pfVerdict == preflight.Abort {
		return m, m.notify("this node cannot be prepared — see the aborts above", toastErr)
	}
	if m.pfVerdict != preflight.Pass && m.pfVerdict != preflight.Adopt {
		return m, m.notify("resolve the decisions above, then plan", toastWarn)
	}
	n := m.selNode
	m.push(screen{kind: scNodePlan, node: n.Name})
	m.loading = loadingState{
		active: true,
		line:   "drafting " + n.Name + "'s baseline plan",
		detail: "read-only · nothing is modified",
		start:  time.Now(),
	}
	m.layout()
	client := m.liveClient
	// generation borrows the live connection like the audit does
	if client != nil {
		m.pauseLive()
	}
	m.auditBusy = true
	knownHosts := filepath.Join(m.store.Dir(), "known_hosts.json")
	name := n.Name
	snap := m.pfSnap
	audit := m.pfAudit
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if client == nil {
			c := ssh.New(n, knownHosts)
			if err := c.Dial(ctx); err != nil {
				return planMsg{node: name, err: err}
			}
			defer c.Close()
			steps, err := plan.Generate(ctx, c, n, snap, audit)
			return planMsg{node: name, steps: steps, err: err}
		}
		steps, err := plan.Generate(ctx, client, n, snap, audit)
		return planMsg{node: name, steps: steps, err: err}
	})
}

// handlePlanDone records the plan step and shows the change list.
// A refused plan leaves the lifecycle untouched and says why.
func (m Model) handlePlanDone(msg planMsg) (tea.Model, tea.Cmd) {
	m.auditBusy = false
	m.loading = loadingState{}
	m.layout()
	if msg.err != nil {
		if m.cur().kind == scNodePlan {
			m.pop()
		}
		var blocked *plan.BlockedError
		if errors.As(msg.err, &blocked) {
			return m, m.notify(fmt.Sprintf("plan blocked · %d decision(s) above", len(blocked.Decisions)), toastWarn)
		}
		return m, m.notify("plan failed: "+shorten(msg.err.Error(), 44), toastErr)
	}
	m.planSteps = msg.steps

	if m.store != nil {
		if n, err := m.store.GetNode(msg.node); err == nil {
			if n.Bootstrap.State == domain.BootstrapNone || n.Bootstrap.State == domain.BootstrapPreflight || n.Bootstrap.State == domain.BootstrapPlan {
				n.Bootstrap = domain.Bootstrap{
					State:    domain.BootstrapPlan,
					Baseline: baseline.Version,
					Verdict:  "planned",
					At:       time.Now(),
				}
			}
			if err := m.store.UpdateNode(n); err != nil {
				return m, m.notify(err.Error(), toastErr)
			}
			m.reloadFleet()
			if m.selNode.Name == msg.node {
				m.selNode = n
			}
		}
	}
	if m.cur().kind != scNodePlan {
		return m, m.notify(fmt.Sprintf("plan %s drafted: %d steps", msg.node, len(msg.steps)), toastOK)
	}
	return m, nil
}

// runSSH execs the system ssh client for the selected node,
// suspending the workspace until the session ends.
func (m Model) runSSH() (tea.Model, tea.Cmd) {
	if m.store == nil {
		return m, m.notify("state store unavailable", toastErr)
	}
	cmd, err := ssh.InteractiveCommand(m.selNode, ssh.InteractiveKnownHostsPath(m.store.Dir()))
	if err != nil {
		return m, m.notify("ssh: "+err.Error(), toastErr)
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return sshFinishedMsg{err: err} })
}

// handleSSHFinished reacts to an interactive session ending. A normal
// shell exit — whatever its code — was visible in the session itself;
// only launch and terminal-restore failures toast.
func (m Model) handleSSHFinished(msg sshFinishedMsg) (tea.Model, tea.Cmd) {
	var exit *exec.ExitError
	if msg.err != nil && !errors.As(msg.err, &exit) {
		return m, m.notify("ssh session: "+shorten(msg.err.Error(), 44), toastErr)
	}
	return m, nil
}
