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

// checkTimeout bounds one whole probe attempt launched from the TUI.
const checkTimeout = 90 * time.Second

// checkDoneMsg reports a probe attempt's outcome. Err is nil when the
// node answered with a fresh snapshot.
type checkDoneMsg struct {
	node string
	snap facts.Node
	err  error
}

// sshFinishedMsg reports the interactive session handed to the system
// ssh client ending.
type sshFinishedMsg struct{ err error }

// newCheckSpinner builds the probe spinner: accent-colored braille
// dots animating while a check runs.
func newCheckSpinner() spinner.Model {
	return spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(accentStyle),
	)
}

// runCheck starts a probe of the selected node, animating until the
// result arrives. One probe runs at a time.
func (m Model) runCheck() (tea.Model, tea.Cmd) {
	if m.probing {
		return m, m.notify("already checking "+m.probingName, toastWarn)
	}
	if m.store == nil {
		return m, m.notify("state store unavailable", toastErr)
	}
	m.probing = true
	m.probingName = m.selNode.Name
	m.fleetSetChecking(m.selNode.Name)
	return m, tea.Batch(m.spinner.Tick, m.beginCheck(m.selNode))
}

// beginCheck probes the node in the background and reports back with
// checkDoneMsg. The probe is read-only; a failure never touches the
// node's last good snapshot.
func (m Model) beginCheck(n domain.Node) tea.Cmd {
	store := m.store
	knownHosts := filepath.Join(store.Dir(), "known_hosts.json")
	name := n.Name
	return func() tea.Msg {
		client := ssh.New(n, knownHosts)
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if err := client.Dial(ctx); err != nil {
			return checkDoneMsg{node: name, err: err}
		}
		defer client.Close()
		snap, err := facts.Probe(ctx, client)
		return checkDoneMsg{node: name, snap: snap, err: err}
	}
}

// handleCheckDone records the probe outcome in the node's record and
// refreshes the fleet: a success stores the snapshot, a failure
// stores why it failed so health never silently reverts to green.
func (m Model) handleCheckDone(msg checkDoneMsg) (tea.Model, tea.Cmd) {
	m.probing = false
	m.probingName = ""
	m.fleetSetChecking("") // clear the probing row before anything else
	if m.store == nil {
		return m, nil
	}
	n, err := m.store.GetNode(msg.node)
	if err != nil {
		return m, m.notify("state error: "+shorten(err.Error(), 40), toastErr)
	}
	if msg.err != nil {
		n.LastCheck = domain.CheckState{At: time.Now(), Error: msg.err.Error()}
	} else {
		n.Facts = msg.snap
		n.LastCheck = domain.CheckState{At: time.Now()}
	}
	if err := m.store.UpdateNode(n); err != nil {
		return m, m.notify(err.Error(), toastErr)
	}
	m.reloadFleet()
	if m.cur().kind == scNode && m.cur().node == msg.node {
		m.selNode = n
	}
	if msg.err != nil {
		return m, m.notify("check failed: "+shorten(msg.err.Error(), 44), toastErr)
	}
	return m, m.notify("checked "+msg.node, toastOK)
}

// fleetSetChecking rebuilds the fleet rows, marking the named node
// as being probed; an empty name clears every flag.
func (m *Model) fleetSetChecking(name string) {
	items := make([]list.Item, 0, len(m.nodes))
	for _, n := range m.nodes {
		items = append(items, fleetItem{node: n, checking: n.Name == name})
	}
	m.fleet.SetItems(items)
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
