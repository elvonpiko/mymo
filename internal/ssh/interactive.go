package ssh

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
)

// ErrNoSSHBinary reports that the system ssh client — the one mymo
// execs for interactive sessions — is not installed.
var ErrNoSSHBinary = errors.New("no ssh executable in PATH")

// InteractiveCommand builds the system ssh client invocation that
// opens an interactive shell on the node. The session inherits the
// caller's terminal; mymo's own transport does not carry interactive
// PTY sessions, so it hands off to the user's ssh instead.
//
// The exec'd ssh keeps its host-key trust store at knownHostsPath,
// following the same policy as the transport: first contact is
// accepted and remembered, any later mismatch is refused. Point it at
// a file inside mymo's state directory so the user's own
// ~/.ssh/known_hosts is never touched.
func InteractiveCommand(node domain.Node, knownHostsPath string) (*exec.Cmd, error) {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, ErrNoSSHBinary
	}
	args := []string{
		"-p", strconv.Itoa(node.Port),
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if node.Auth == domain.AuthKey {
		args = append(args, "-i", node.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, node.User+"@"+node.Host)
	return exec.Command(bin, args...), nil
}

// knownHostsFile is the conventional OpenSSH-format trust store name
// inside mymo's state directory, next to the transport's
// known_hosts.json.
const knownHostsFile = "known_hosts"

// InteractiveKnownHostsPath places the interactive trust store inside
// the given mymo state directory.
func InteractiveKnownHostsPath(stateDir string) string {
	return filepath.Join(stateDir, knownHostsFile)
}

// ExecCommand builds the system ssh invocation that runs one remote
// command interactively — the application shell hands its PTY to
// docker exec inside the node, so the operator's terminal is the
// container's terminal. sudo wraps the exec when the operator is
// not root, keeping the same privilege rule as every mymo command.
func ExecCommand(node domain.Node, knownHostsPath string, sudo bool, remote ...string) (*exec.Cmd, error) {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, ErrNoSSHBinary
	}
	args := []string{
		"-p", strconv.Itoa(node.Port),
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(defaultDialTimeout/time.Second)),
		"-t", // a shell needs a terminal
	}
	if node.Auth == domain.AuthKey {
		args = append(args, "-i", node.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, node.User+"@"+node.Host)
	if sudo {
		args = append(args, "sudo -n")
	}
	args = append(args, remote...)
	return exec.Command(bin, args...), nil
}
