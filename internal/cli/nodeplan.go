package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// runNodePlan generates and prints a node's preparation plan: the
// exact changes baseline 0.1 would make, from a fresh probe and audit.
// Planning changes nothing; the plan states files, packages, and
// commands in full so it can be reviewed before anything runs. When
// the audit demands decisions, the plan refuses and lists them.
func runNodePlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node plan <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node plan: %v\n", err)
		return exitErr
	}

	fmt.Fprintf(stderr, "planning %s against baseline %s (read-only)...\n", node.Name, baseline.Version)
	client := ssh.New(node, knownHostsPath(store))
	if err := client.Dial(ctx); err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node plan: %v\n", err)
		return exitErr
	}
	defer client.Close()

	snapshot, err := facts.Probe(ctx, client)
	if err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node plan: %v\n", err)
		return exitErr
	}
	audit, err := preflight.Scan(ctx, client)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node plan: audit failed: %v\n", err)
		return exitErr
	}

	steps, err := plan.Generate(ctx, client, node, snapshot, audit)
	var blocked *plan.BlockedError
	if errors.As(err, &blocked) {
		node.Facts = snapshot
		node.LastCheck = domain.CheckState{At: time.Now()}
		if node.Bootstrap.State == domain.BootstrapNone || node.Bootstrap.State == domain.BootstrapPreflight {
			node.Bootstrap = domain.Bootstrap{
				State:    domain.BootstrapPreflight,
				Baseline: baseline.Version,
				Verdict:  preflight.Decide.String(),
				At:       time.Now(),
			}
		}
		_ = store.UpdateNode(node)
		printPlanBlocked(stdout, blocked)
		return pfExitDecide
	}
	if err != nil {
		fmt.Fprintf(stderr, "mymo node plan: %v\n", err)
		return exitErr
	}

	// the plan is recorded at the plan step: generated, awaiting
	// confirmation. Deeper lifecycle positions are never rewound.
	node.Facts = snapshot
	node.LastCheck = domain.CheckState{At: time.Now()}
	if node.Bootstrap.State == domain.BootstrapNone || node.Bootstrap.State == domain.BootstrapPreflight {
		node.Bootstrap = domain.Bootstrap{
			State:    domain.BootstrapPlan,
			Baseline: baseline.Version,
			Verdict:  "planned",
			At:       time.Now(),
		}
	}
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node plan: %v\n", err)
		return exitErr
	}

	printPlan(stdout, node.Name, steps)
	return exitOK
}

// printPlan renders the change list in full: one block per step with
// its detail, files (path, mode, owner), packages, commands, and the
// gate that applies to it.
func printPlan(w io.Writer, name string, steps []plan.Step) {
	fmt.Fprintf(w, "plan · %s · baseline %s · %d steps\n\n", name, baseline.Version, len(steps))
	for i, s := range steps {
		fmt.Fprintf(w, "%d. %s\n", i+1, s.Title)
		fmt.Fprintf(w, "   %s\n", s.Detail)
		for _, f := range s.Files {
			fmt.Fprintf(w, "   writes %s (%s, %s)\n", f.Path, f.Mode, f.Owner)
			for _, line := range strings.Split(strings.TrimRight(f.Content, "\n"), "\n") {
				fmt.Fprintf(w, "     | %s\n", line)
			}
		}
		if len(s.Packages) > 0 {
			fmt.Fprintf(w, "   installs %s\n", strings.Join(s.Packages, ", "))
		}
		for _, argv := range s.Exec {
			fmt.Fprintf(w, "   runs %s\n", strings.Join(argv, " "))
		}
		if s.Gate != "" {
			fmt.Fprintf(w, "   gate: %s\n", s.Gate)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "this plan changes nothing until it is confirmed;")
	fmt.Fprintln(w, "apply ships with the next phase.")
}

// printPlanBlocked names every decision standing in the way.
func printPlanBlocked(w io.Writer, blocked *plan.BlockedError) {
	fmt.Fprintln(w, "planning is blocked — resolve these on the node:")
	fmt.Fprintln(w)
	for _, c := range blocked.Decisions {
		fmt.Fprintf(w, "  %-6s %-14s %s\n", c.Outcome, c.Title, c.Detail)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "mymo plans nothing past a decision; re-run after resolving them.")
}
