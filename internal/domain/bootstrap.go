package domain

import (
	"fmt"
	"time"
)

// BootstrapState is where a node sits in the App Host preparation
// lifecycle:
//
//	DISCOVER → PREFLIGHT → PLAN → CONFIRM → APPLY → VERIFY → READY
//
// mymo only ever moves one step at a time, forward, and nothing is
// applied before the plan is confirmed by a human. The node is marked
// READY only after verification succeeds.
type BootstrapState string

const (
	// BootstrapNone means no preparation has been recorded; the node
	// is observed only.
	BootstrapNone BootstrapState = ""
	// BootstrapPreflight means the read-only audit has run and its
	// verdict is recorded.
	BootstrapPreflight BootstrapState = "preflight"
	// BootstrapPlan means the exact change list was generated and is
	// waiting for the user's confirmation.
	BootstrapPlan BootstrapState = "plan"
	// BootstrapConfirmed means the user approved the plan. No
	// confirmation, no mutation — this state is the gate apply reads.
	BootstrapConfirmed BootstrapState = "confirmed"
	// BootstrapApplying means the baseline's mutations are running
	// (Phase 4). An interrupted apply is reported, never assumed done.
	BootstrapApplying BootstrapState = "applying"
	// BootstrapVerified means apply finished and every control was
	// verified — SSH access through the new path included.
	BootstrapVerified BootstrapState = "verified"
	// BootstrapReady means the node is promoted to App Host.
	BootstrapReady BootstrapState = "ready"
)

// bootstrapOrder is the one legal path. "none" precedes "preflight"
// but is not a step of the lifecycle — it is the unstarted state.
var bootstrapOrder = []BootstrapState{
	BootstrapPreflight,
	BootstrapPlan,
	BootstrapConfirmed,
	BootstrapApplying,
	BootstrapVerified,
	BootstrapReady,
}

// NextBootstrap returns the single next lifecycle state after from.
// Steps are never skipped — a node cannot reach "confirmed" without
// passing through "plan", and "ready" only after "verified".
func NextBootstrap(from BootstrapState) (BootstrapState, error) {
	for i, s := range bootstrapOrder {
		if from == s {
			if i+1 >= len(bootstrapOrder) {
				return from, fmt.Errorf("bootstrap already ready")
			}
			return bootstrapOrder[i+1], nil
		}
	}
	return BootstrapPreflight, nil // unstarted nodes begin with the audit
}

// BootstrapRank is a state's position in the lifecycle order; the
// unstarted state ranks below everything. Callers advance a node
// only forward by comparing ranks.
func BootstrapRank(s BootstrapState) int {
	for i, v := range bootstrapOrder {
		if s == v {
			return i + 1
		}
	}
	return 0
}

// Bootstrap is a node's recorded position in the preparation
// lifecycle, persisted with the node so an interrupted bootstrap is
// visible rather than guessed about.
type Bootstrap struct {
	State    BootstrapState `json:"state,omitempty"`
	Baseline string         `json:"baseline,omitempty"`
	Verdict  string         `json:"verdict,omitempty"`
	At       time.Time      `json:"at,omitzero"`
}

// Promotable reports whether a node may be marked App Host: only a
// bootstrap that reached READY earns the promotion. Observe is the
// honest mode for everything else.
func (n Node) Promotable() bool {
	return n.Bootstrap.State == BootstrapReady
}

// Promote marks the node as an App Host. It refuses unless the
// bootstrap completed and verified; a node is never implicitly
// promoted from observe.
func (n *Node) Promote() error {
	if !n.Promotable() {
		return fmt.Errorf("node %q is not prepared: bootstrap state is %q", n.Name, n.Bootstrap.State)
	}
	n.Mode = ModeAppHost
	return nil
}
