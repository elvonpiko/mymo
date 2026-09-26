package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// knownHostsPath is where mymo's transport records host keys on first
// contact (TOFU) inside the state directory.
func knownHostsPath(store *state.Store) string {
	return filepath.Join(store.Dir(), "known_hosts.json")
}

// runNodeCheck probes a node over SSH and stores the discovered facts.
// The status line goes to stderr so stdout stays clean output.
func runNodeCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node check <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node check: %v\n", err)
		return exitErr
	}

	fmt.Fprintf(stderr, "probing %s (%s)...\n", node.Name, node.Address())
	client := ssh.New(node, knownHostsPath(store))
	if err := client.Dial(ctx); err != nil {
		fmt.Fprintf(stderr, "mymo node check: %v\n", err)
		return exitErr
	}
	defer client.Close()

	snapshot, err := facts.Probe(ctx, client)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node check: %v\n", err)
		return exitErr
	}
	node.Facts = snapshot
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node check: %v\n", err)
		return exitErr
	}
	printFacts(stdout, snapshot)
	return exitOK
}

// runNodeSSH opens an interactive shell on the node by execing the
// system ssh client. An interactive shell needs a real terminal, so
// the child inherits the process streams — the one place the CLI
// bypasses the passed writers; diagnostics printed before the exec
// still go through stderr. The child's exit code becomes mymo's.
//
// The exec'd ssh keeps its own host-key trust store inside mymo's
// state directory, following the transport's policy: first contact is
// accepted and remembered, any later mismatch is refused. The user's
// ~/.ssh/known_hosts is never touched.
func runNodeSSH(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node ssh <name>")
		return exitUsage
	}
	store, err := state.Open()
	if err != nil {
		fmt.Fprintf(stderr, "mymo node ssh: %v\n", err)
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node ssh: %v\n", err)
		return exitErr
	}

	bin, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "mymo node ssh: no ssh executable in PATH")
		return exitErr
	}
	sshArgs := []string{
		"-p", strconv.Itoa(node.Port),
		"-o", "UserKnownHostsFile=" + filepath.Join(store.Dir(), "known_hosts"),
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if node.Auth == domain.AuthKey {
		sshArgs = append(sshArgs, "-i", node.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	sshArgs = append(sshArgs, node.User+"@"+node.Host)

	cmd := exec.Command(bin, sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(stderr, "mymo node ssh: %v\n", err)
		return exitErr
	}
	return exitOK
}
