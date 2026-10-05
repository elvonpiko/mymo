package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/apply"
	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// applyConfirmIn is the confirmation prompt's input; a var so tests
// can refuse the way pipelines do — EOF and anything but the exact
// name abort, and nothing changes.
var applyConfirmIn io.Reader = os.Stdin

// runNodeApply applies the preparation plan to a node: a fresh probe
// and audit, the plan regenerated from live reality and printed in
// full, the node's name typed as the human confirmation, the mymo
// key generated locally, the steps executed with visible progress,
// then the effective-state verification. The lifecycle states are
// recorded as they truly advance, and a failure records exactly
// where the apply stopped.
func runNodeApply(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node apply <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}

	switch node.Bootstrap.State {
	case domain.BootstrapPlan, domain.BootstrapConfirmed, domain.BootstrapApplying:
		// a plan exists (or a previous apply stopped early); the
		// typed name below is the confirmation
	default:
		if node.Bootstrap.State == domain.BootstrapReady {
			fmt.Fprintf(stderr, "mymo node apply: %s is already prepared (baseline %s, verified)\n", node.Name, node.Bootstrap.Baseline)
		} else {
			fmt.Fprintf(stderr, "mymo node apply: generate the plan first — mymo node plan %s\n", node.Name)
		}
		return exitErr
	}

	fmt.Fprintf(stderr, "applying %s against baseline %s...\n", node.Name, baseline.Version)
	client := ssh.New(node, knownHostsPath(store))
	client.WithProgress(dialNarrator(stderr))
	if err := client.Dial(ctx); err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}
	defer client.Close()

	snapshot, err := facts.Probe(ctx, client)
	if err != nil {
		recordFailedCheck(store, node, err)
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}
	audit, err := preflight.Scan(ctx, client)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node apply: audit failed: %v\n", err)
		return exitErr
	}
	steps, err := plan.Generate(ctx, client, node, snapshot, audit)
	var blocked *plan.BlockedError
	if errors.As(err, &blocked) {
		node.Facts = snapshot
		node.LastCheck = domain.CheckState{At: time.Now()}
		_ = store.UpdateNode(node)
		printPlanBlocked(stdout, blocked)
		fmt.Fprintln(stderr, "mymo applies nothing past a decision; resolve these on the node first.")
		return pfExitDecide
	}
	if err != nil {
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}

	printPlan(stdout, node.Name, steps)
	if node.User == "root" {
		fmt.Fprintln(stderr, "\nwarning: you connect as root — after apply, sshd refuses root logins;")
		fmt.Fprintln(stderr, "access continues as mymo (mymo holds that key) and through the provider's console.")
	}

	// the human gate: the typed name, nothing shorter
	fmt.Fprintf(stderr, "\ntype %s to apply (anything else aborts): ", node.Name)
	reader := bufio.NewReader(applyConfirmIn)
	typed, _ := reader.ReadString('\n')
	if strings.TrimSpace(typed) != node.Name {
		fmt.Fprintf(stderr, "\naborted — %s is untouched.\n", node.Name)
		return exitErr
	}

	// the mymo key pair is generated locally; the private half never
	// leaves this machine
	keyPath := mymoKeyPath(store, node.Name)
	pub, err := ssh.GenerateEd25519(keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}

	opts := apply.Options{
		Sudo:          snapshot.User != "root",
		MymoPublicKey: pub,
		Now:           time.Now(),
		Prover: apply.SSHProver{
			Node:       node,
			PrivateKey: keyPath,
			KnownHosts: knownHostsPath(store),
		},
		Progress: func(line string) { fmt.Fprintln(stderr, "  "+line) },
	}

	// the states advance only forward: confirmed, then applying
	if node.Bootstrap.State == domain.BootstrapPlan {
		node.Bootstrap = domain.Bootstrap{State: domain.BootstrapConfirmed, Baseline: baseline.Version, Verdict: "confirmed", At: time.Now()}
		_ = store.UpdateNode(node)
	}
	node.Bootstrap = domain.Bootstrap{State: domain.BootstrapApplying, Baseline: baseline.Version, Verdict: "applying", At: time.Now()}
	node.Facts = snapshot
	node.LastCheck = domain.CheckState{At: time.Now()}
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}

	res := apply.Apply(ctx, client, steps, opts)
	if res.Failed {
		node.Bootstrap.Verdict = "failed at " + res.FailedAt
		node.Bootstrap.At = time.Now()
		_ = store.UpdateNode(node)
		printApplyReport(stderr, res)
		fmt.Fprintln(stderr, "\nthe steps after the failure were never attempted;")
		fmt.Fprintln(stderr, "the operator's current access is untouched; re-run when resolved.")
		return exitErr
	}
	printApplyReport(stdout, res)

	fmt.Fprintln(stderr, "\nverifying the effective state...")
	checks, verified := apply.Verify(ctx, client, opts, baseline.Version)
	if !verified {
		node.Bootstrap.Verdict = "verify failed"
		node.Bootstrap.At = time.Now()
		_ = store.UpdateNode(node)
		printVerifyChecks(stderr, checks)
		fmt.Fprintln(stderr, "\nthe baseline ran but did not verify — mymo will not call this node ready.")
		return exitErr
	}

	node.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: baseline.Version, Verdict: "ready", At: time.Now()}
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node apply: %v\n", err)
		return exitErr
	}
	printVerifyChecks(stdout, checks)
	fmt.Fprintf(stdout, "\nmymo security baseline %s applied and verified on %s.\n", baseline.Version, node.Name)
	fmt.Fprintf(stdout, "the mymo key is kept at %s\n", keyPath)
	fmt.Fprintf(stdout, "promote to app host when ready: mymo node promote %s\n", node.Name)
	return exitOK
}

// runNodePromote moves a verified node to app host mode. The
// promotion is explicit and refuses anything the bootstrap has not
// earned.
func runNodePromote(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node promote <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	node, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node promote: %v\n", err)
		return exitErr
	}
	before := node.Mode
	if err := node.Promote(); err != nil {
		fmt.Fprintf(stderr, "mymo node promote: %v\n", err)
		return exitErr
	}
	if err := store.UpdateNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node promote: %v\n", err)
		return exitErr
	}
	if before == node.Mode {
		fmt.Fprintf(stdout, "%s is already an app host.\n", node.Name)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s is now an app host.\n", node.Name)
	fmt.Fprintln(stdout, "deploys ship with the next phase.")
	return exitOK
}

// mymoKeyPath is where the mymo key pair for one node lives.
func mymoKeyPath(store *state.Store, name string) string {
	return filepath.Join(store.Dir(), "keys", name+".key")
}

// printApplyReport shows each step's outcome: done, kept, failed, or
// blocked — with the failure's reason in full.
func printApplyReport(w io.Writer, res apply.Result) {
	for _, s := range res.Steps {
		switch s.State {
		case apply.StepDone:
			fmt.Fprintf(w, "  %-6s %s\n", "done", s.Title)
		case apply.StepKept:
			fmt.Fprintf(w, "  %-6s %s\n", "kept", s.Title)
		case apply.StepFailed:
			fmt.Fprintf(w, "  %-6s %s\n", "FAILED", s.Title)
			fmt.Fprintf(w, "         %s\n", s.Note)
		default:
			fmt.Fprintf(w, "  %-6s %s\n", "blocked", s.Title)
		}
	}
	if res.GateProven {
		fmt.Fprintln(w, "  gate   the mymo key was proven on a second connection before the reload")
	}
}

// printVerifyChecks renders the verification rows: what the system
// now enforces, effective state, not config file claims.
func printVerifyChecks(w io.Writer, checks []preflight.Check) {
	for _, c := range checks {
		mark := "ok"
		if c.Outcome != preflight.Pass {
			mark = "FAILED"
		}
		fmt.Fprintf(w, "  %-6s %-18s %s\n", mark, c.Title, c.Detail)
	}
}
