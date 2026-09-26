package tui

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
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
	m.liveSeq++
	m.liveClient = msg.client
	m.liveErr = ""
	// seed the live memory readout from the fresh snapshot so the card
	// never shows a blank while the first sample is in flight
	m.liveCur = liveSample{memAvail: msg.snap.MemAvail, memTotal: msg.snap.MemTotal}
	return m, m.sampleLive(m.liveSeq)
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
