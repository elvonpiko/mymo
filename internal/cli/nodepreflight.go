package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// preflight exit codes carry the verdict for scripts: 0 the path is
// clear (pass or adopt), 2 a human decision is required, 1 the node
// is unsuitable. Discovery or transport failures are also 1.
const (
	pfExitAbort  = exitErr
	pfExitDecide = 2
)

// runNodePreflight runs the App Host preflight on a node: a fresh
// discovery probe plus the deep read-only audit, evaluated into
// per-check verdicts against the pinned baseline. Nothing on the
// node is modified. The verdict is recorded on the node so an
// interrupted preparation leaves a visible trail.
func runNodePreflight(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node preflight <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node preflight: %v\n", err)
		return exitErr
	}

	fmt.Fprintf(stderr, "preflight %s against baseline %s (read-only)...\n", node.Name, baseline.Version)
	client := ssh.New(node, knownHostsPath(store))
	client.WithProgress(dialNarrator(stderr))
	if err := client.Dial(ctx); err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node preflight: %v\n", err)
		return exitErr
	}
	defer client.Close()

	// a fresh snapshot first: verdicts speak about the node as it is
	// right now, not as of the last visit
	snapshot, err := facts.Probe(ctx, client)
	if err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node preflight: %v\n", err)
		return exitErr
	}
	audit, err := preflight.Scan(ctx, client)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node preflight: audit failed: %v\n", err)
		return exitErr
	}

	checks := preflight.Evaluate(snapshot, audit)
	verdict := preflight.Verdict(checks)

	node.Facts = snapshot
	node.LastCheck = domain.CheckState{At: time.Now()}
	// the audit is recorded while the node is at or before the
	// preflight step, and refreshed by re-running it; a node already
	// deeper in the lifecycle keeps its position — preflight never
	// rewinds preparation
	if node.Bootstrap.State == domain.BootstrapNone || node.Bootstrap.State == domain.BootstrapPreflight {
		node.Bootstrap = domain.Bootstrap{
			State:    domain.BootstrapPreflight,
			Baseline: baseline.Version,
			Verdict:  verdict.String(),
			At:       time.Now(),
		}
	}
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node preflight: %v\n", err)
		return exitErr
	}

	printPreflight(stdout, node.Name, checks, verdict)

	switch verdict {
	case preflight.Decide:
		return pfExitDecide
	case preflight.Abort:
		return pfExitAbort
	default:
		return exitOK
	}
}

// printPreflight renders the audit: one line per check, the verdict
// last. Glyphs match the TUI's colors in plain ASCII.
func printPreflight(w io.Writer, name string, checks []preflight.Check, verdict preflight.Outcome) {
	fmt.Fprintf(w, "preflight · %s · baseline %s\n\n", name, baseline.Version)
	for _, c := range checks {
		glyph := "ok"
		switch c.Outcome {
		case preflight.Adopt:
			glyph = "adopt"
		case preflight.Decide:
			glyph = "decide"
		case preflight.Abort:
			glyph = "abort"
		}
		fmt.Fprintf(w, "  %-6s %-14s %s\n", glyph, c.Title, c.Detail)
	}
	fmt.Fprintf(w, "\nverdict: %s", verdict)
	switch verdict {
	case preflight.Pass:
		fmt.Fprintln(w, " — the path is clear; preparing continues with a plan to review")
	case preflight.Adopt:
		fmt.Fprintln(w, " — existing components can be adopted; the plan says exactly what changes")
	case preflight.Decide:
		fmt.Fprintln(w, " — your call is required before anything is planned")
	default:
		fmt.Fprintln(w, " — this node cannot be prepared as an app host")
	}
}
