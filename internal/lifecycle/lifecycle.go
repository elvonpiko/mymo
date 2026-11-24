// Package lifecycle is the one place a node's bootstrap record moves.
// The CLI and the TUI both call it, then persist the node it returns.
// Nothing here touches a server or a disk: the record is the whole job.
//
// The rules:
//
//   - an audit or a plan never rewinds a lifecycle that has moved past it
//   - an apply of a node that is already prepared (an upgrade) does not
//     step backward through the setup states
//   - a first setup that stops stays at applying, so the node page can
//     offer Resume and the CLI can run apply again
//   - ready is written only after verification
package lifecycle

import (
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
)

// NotePreflight records an audit while the node is still at the start.
// A deeper state is returned unchanged.
func NotePreflight(n domain.Node, verdict string, now time.Time) domain.Node {
	switch n.Bootstrap.State {
	case domain.BootstrapNone, domain.BootstrapPreflight:
		n.Bootstrap = domain.Bootstrap{
			State:    domain.BootstrapPreflight,
			Baseline: baseline.Version,
			Verdict:  verdict,
			At:       now,
		}
	}
	return n
}

// NotePlan records a generated plan while the node has not passed the
// plan step. Applying, verified, and ready are left where they are.
func NotePlan(n domain.Node, now time.Time) domain.Node {
	switch n.Bootstrap.State {
	case domain.BootstrapNone, domain.BootstrapPreflight, domain.BootstrapPlan:
		n.Bootstrap = domain.Bootstrap{
			State:    domain.BootstrapPlan,
			Baseline: baseline.Version,
			Verdict:  "planned",
			At:       now,
		}
	}
	return n
}

// BeginApply records that an apply is about to change the box. A
// prepared node being upgraded keeps its state and says "upgrading".
// A first setup moves to applying — through confirmed when it was
// still only a plan, so the record never skips the gate.
func BeginApply(n domain.Node, upgrading bool, now time.Time) domain.Node {
	if upgrading {
		n.Bootstrap.Verdict = "upgrading"
		n.Bootstrap.At = now
		return n
	}
	if n.Bootstrap.State == domain.BootstrapPlan {
		n.Bootstrap = domain.Bootstrap{
			State:    domain.BootstrapConfirmed,
			Baseline: baseline.Version,
			Verdict:  "confirmed",
			At:       now,
		}
	}
	n.Bootstrap = domain.Bootstrap{
		State:    domain.BootstrapApplying,
		Baseline: baseline.Version,
		Verdict:  "applying",
		At:       now,
	}
	return n
}

// StopApply records where an apply stopped. A prepared node stays
// prepared. A first setup stays at applying, with a verdict that
// names the resume.
func StopApply(n domain.Node, where string, upgrading bool, now time.Time) domain.Node {
	if prepared(n) || upgrading {
		if upgrading {
			n.Bootstrap.Verdict = "upgrade stopped at " + where
		} else {
			n.Bootstrap.Verdict = "apply stopped at " + where
		}
		n.Bootstrap.At = now
		return n
	}
	if n.Bootstrap.Baseline == "" {
		n.Bootstrap.Baseline = baseline.Version
	}
	n.Bootstrap.State = domain.BootstrapApplying
	n.Bootstrap.Verdict = "apply stopped at " + where + " — resume from the node"
	n.Bootstrap.At = now
	return n
}

// VerifyFailed records that the apply ran and the live checks did not
// hold. The same state rule as StopApply: a prepared node stays, a
// first setup stays resumable.
func VerifyFailed(n domain.Node, upgrading bool, now time.Time) domain.Node {
	if prepared(n) || upgrading {
		if upgrading {
			n.Bootstrap.Verdict = "upgrade did not verify"
		} else {
			n.Bootstrap.Verdict = "verify failed"
		}
		n.Bootstrap.At = now
		return n
	}
	if n.Bootstrap.Baseline == "" {
		n.Bootstrap.Baseline = baseline.Version
	}
	n.Bootstrap.State = domain.BootstrapApplying
	n.Bootstrap.Verdict = "verify failed — resume from the node"
	n.Bootstrap.At = now
	return n
}

// MarkReady records a verified baseline. This is the only writer of
// the ready state.
func MarkReady(n domain.Node, now time.Time) domain.Node {
	n.Bootstrap = domain.Bootstrap{
		State:    domain.BootstrapReady,
		Baseline: baseline.Version,
		Verdict:  "ready",
		At:       now,
	}
	return n
}

func prepared(n domain.Node) bool {
	return n.Bootstrap.State == domain.BootstrapVerified ||
		n.Bootstrap.State == domain.BootstrapReady
}
