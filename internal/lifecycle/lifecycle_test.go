package lifecycle

import (
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
)

func TestNotePreflightDoesNotRewind(t *testing.T) {
	now := time.Now()
	n := domain.Node{Bootstrap: domain.Bootstrap{State: domain.BootstrapApplying, Baseline: "0.0", Verdict: "applying"}}
	got := NotePreflight(n, "ok", now)
	if got.Bootstrap.State != domain.BootstrapApplying || got.Bootstrap.Baseline != "0.0" {
		t.Fatalf("audit rewound an apply: %+v", got.Bootstrap)
	}
	fresh := NotePreflight(domain.Node{}, "ok", now)
	if fresh.Bootstrap.State != domain.BootstrapPreflight || fresh.Bootstrap.Baseline != baseline.Version {
		t.Fatalf("fresh audit = %+v", fresh.Bootstrap)
	}
}

func TestBeginApplyUpgradesInPlace(t *testing.T) {
	now := time.Now()
	n := domain.Node{Bootstrap: domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.0", Verdict: "ready"}}
	got := BeginApply(n, true, now)
	if got.Bootstrap.State != domain.BootstrapReady || got.Bootstrap.Verdict != "upgrading" {
		t.Fatalf("upgrade moved the node: %+v", got.Bootstrap)
	}
	first := BeginApply(domain.Node{Bootstrap: domain.Bootstrap{State: domain.BootstrapPlan}}, false, now)
	if first.Bootstrap.State != domain.BootstrapApplying || first.Bootstrap.Baseline != baseline.Version {
		t.Fatalf("first apply = %+v", first.Bootstrap)
	}
}

func TestStopApplyStaysResumable(t *testing.T) {
	now := time.Now()
	n := BeginApply(domain.Node{Bootstrap: domain.Bootstrap{State: domain.BootstrapPlan}}, false, now)
	got := StopApply(n, "caddy", false, now)
	if got.Bootstrap.State != domain.BootstrapApplying {
		t.Fatalf("state = %s, want applying", got.Bootstrap.State)
	}
	if got.Bootstrap.Verdict != "apply stopped at caddy — resume from the node" {
		t.Fatalf("verdict = %q", got.Bootstrap.Verdict)
	}
	ready := domain.Node{Bootstrap: domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.0"}}
	up := StopApply(BeginApply(ready, true, now), "caddy", true, now)
	if up.Bootstrap.State != domain.BootstrapReady || up.Bootstrap.Verdict != "upgrade stopped at caddy" {
		t.Fatalf("upgrade failure = %+v", up.Bootstrap)
	}
}

func TestMarkReady(t *testing.T) {
	got := MarkReady(domain.Node{}, time.Now())
	if got.Bootstrap.State != domain.BootstrapReady || got.Bootstrap.Baseline != baseline.Version || got.Bootstrap.Verdict != "ready" {
		t.Fatalf("ready = %+v", got.Bootstrap)
	}
}
