package domain

import "testing"

func TestNextBootstrapWalksOneStepAtATime(t *testing.T) {
	cases := []struct {
		from BootstrapState
		want BootstrapState
	}{
		{BootstrapNone, BootstrapPreflight},
		{BootstrapPreflight, BootstrapPlan},
		{BootstrapPlan, BootstrapConfirmed},
		{BootstrapConfirmed, BootstrapApplying},
		{BootstrapApplying, BootstrapVerified},
		{BootstrapVerified, BootstrapReady},
	}
	for _, tc := range cases {
		got, err := NextBootstrap(tc.from)
		if err != nil || got != tc.want {
			t.Errorf("NextBootstrap(%q) = %q, %v; want %q, nil", tc.from, got, err, tc.want)
		}
	}
}

func TestNextBootstrapRefusesPastReady(t *testing.T) {
	if got, err := NextBootstrap(BootstrapReady); err == nil || got != BootstrapReady {
		t.Errorf("ready must be terminal, got %q, %v", got, err)
	}
}

func TestBootstrapNeverSkipsConfirmation(t *testing.T) {
	// walking the legal path from unstarted to ready takes exactly
	// six steps; any shorter path is a bug
	steps := 0
	for s := BootstrapNone; steps < 10; steps++ {
		if s == BootstrapReady {
			break
		}
		next, err := NextBootstrap(s)
		if err != nil {
			t.Fatalf("step %d from %q: %v", steps, s, err)
		}
		s = next
	}
	if steps != 6 {
		t.Errorf("unstarted to ready took %d steps, want 6", steps)
	}
}

func TestPromoteRequiresVerifiedBootstrap(t *testing.T) {
	n := Node{Name: "web-1", Mode: ModeObserve}
	if err := n.Promote(); err == nil {
		t.Fatal("observe node promoted without a completed bootstrap")
	}
	n.Bootstrap.State = BootstrapVerified
	if err := n.Promote(); err == nil {
		t.Fatal("verified-but-not-ready node promoted")
	}
	n.Bootstrap.State = BootstrapReady
	if err := n.Promote(); err != nil {
		t.Fatalf("ready node refused promotion: %v", err)
	}
	if n.Mode != ModeAppHost {
		t.Errorf("mode = %q, want app-host", n.Mode)
	}
}
