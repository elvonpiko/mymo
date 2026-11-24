package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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

// runNodeCheck probes one node, or every node when no name is given.
// The walk is one SSH at a time. A node that does not answer is
// recorded and the walk continues; the exit code is an error when
// any probe failed. The status line goes to stderr so stdout stays
// the facts.
func runNodeCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: mymo node check [name]")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	if len(args) == 1 {
		node, err := store.GetNode(args[0])
		if err != nil {
			fmt.Fprintf(stderr, "mymo node check: %v\n", err)
			return exitErr
		}
		if err := checkNode(ctx, store, node, stdout, stderr, false); err != nil {
			fmt.Fprintf(stderr, "mymo node check: %v\n", err)
			return exitErr
		}
		return exitOK
	}
	nodes, err := store.LoadNodes()
	if err != nil {
		fmt.Fprintf(stderr, "mymo node check: %v\n", err)
		return exitErr
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stderr, "mymo node check: no nodes")
		return exitOK
	}
	failed := 0
	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(stderr, "mymo node check: stopped after %d of %d\n", i, len(nodes))
			return exitErr
		}
		if err := checkNode(ctx, store, node, stdout, stderr, true); err != nil {
			fmt.Fprintf(stderr, "mymo node check: %s: %v\n", node.Name, err)
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "checked %d · %d failed\n", len(nodes), failed)
		return exitErr
	}
	fmt.Fprintf(stderr, "checked %d\n", len(nodes))
	return exitOK
}

// checkNode probes one node and stores the outcome. header separates
// a fleet walk's facts from the next node's. A dial or probe failure
// is recorded on the node and returned; the last good facts stay.
func checkNode(ctx context.Context, store *state.Store, node domain.Node, stdout, stderr io.Writer, header bool) error {
	fmt.Fprintf(stderr, "probing %s (%s)...\n", node.Name, node.Address())
	client := ssh.New(node, knownHostsPath(store))
	client.WithProgress(dialNarrator(stderr))
	if err := client.Dial(ctx); err != nil {
		recordFailedCheck(store, node, err)
		return err
	}
	defer client.Close()

	snapshot, err := facts.Probe(ctx, client)
	if err != nil {
		recordFailedCheck(store, node, err)
		return err
	}
	node.Facts = snapshot
	node.LastCheck = domain.CheckState{At: time.Now()}
	if err := store.UpdateNode(node); err != nil {
		return err
	}
	if header {
		fmt.Fprintf(stdout, "# %s\n", node.Name)
	}
	printFacts(stdout, snapshot)
	if header {
		fmt.Fprintln(stdout)
	}
	return nil
}

// recordFailedCheck stores why a probe attempt failed, keeping the
// node's last good facts — health must never silently revert to green.
// Storage errors are secondary to the probe error and are ignored.
func recordFailedCheck(store *state.Store, node domain.Node, err error) {
	node.LastCheck = domain.CheckState{At: time.Now(), Error: err.Error()}
	_ = store.UpdateNode(node)
}

// runNodeSSH opens an interactive shell on the node by execing the
// system ssh client. An interactive shell needs a real terminal, so
// the child inherits the process streams — the one place the CLI
// bypasses the passed writers; diagnostics printed before the exec
// still go through stderr. The child's exit code becomes mymo's.
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

	cmd, err := ssh.InteractiveCommand(node, ssh.InteractiveKnownHostsPath(store.Dir()))
	if err != nil {
		fmt.Fprintf(stderr, "mymo node ssh: %v\n", err)
		return exitErr
	}
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
