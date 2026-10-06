package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/apply"
	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/preflight"
	"github.com/elvonpiko/mymo/internal/state"
)

// seededNode adds a node through the CLI, then moves its recorded
// bootstrap state directly — the position an interrupted flow leaves
// behind, which the apply gate must read honestly.
func seededNode(t *testing.T, s *session, name string, b domain.Bootstrap) {
	t.Helper()
	code, out, _ := s.run(t, "node", "add",
		"-name", name, "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	if code != exitOK {
		t.Fatalf("node add failed for %s: %q", name, out)
	}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.GetNode(name)
	if err != nil {
		t.Fatal(err)
	}
	n.Bootstrap = b
	if err := store.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
}

func TestNodeApplyUsage(t *testing.T) {
	s := newSession(t)
	code, _, errb := s.run(t, "node", "apply")
	if code != exitUsage || !strings.Contains(errb, "usage: mymo node apply") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestNodeApplyNeedsAPlanFirst(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", domain.Bootstrap{})
	code, _, errb := s.run(t, "node", "apply", "web-1")
	if code != exitErr {
		t.Fatalf("code=%d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "generate the plan first") {
		t.Errorf("err = %q", errb)
	}
}

func TestNodeApplyRefusesAReadyNode(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", domain.Bootstrap{
		State: domain.BootstrapReady, Baseline: baseline.Version, At: time.Now(),
	})
	code, _, errb := s.run(t, "node", "apply", "web-1")
	if code != exitErr {
		t.Fatalf("code=%d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "already prepared") {
		t.Errorf("err = %q", errb)
	}
}

func TestNodePromoteRequiresReady(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", domain.Bootstrap{})
	code, _, errb := s.run(t, "node", "promote", "web-1")
	if code != exitErr {
		t.Fatalf("code=%d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "is not prepared") {
		t.Errorf("err = %q", errb)
	}
}

func TestNodePromoteMarksAndIsHonestAboutRepeats(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", domain.Bootstrap{
		State: domain.BootstrapReady, Baseline: baseline.Version, At: time.Now(),
	})
	code, out, errb := s.run(t, "node", "promote", "web-1")
	if code != exitOK || !strings.Contains(out, "is now an app host") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errb)
	}
	code, out, _ = s.run(t, "node", "promote", "web-1")
	if code != exitOK || !strings.Contains(out, "already an app host") {
		t.Fatalf("repeat promote: code=%d out=%q", code, out)
	}
	// the promotion must have actually stuck in state
	code, out, _ = s.run(t, "node", "list")
	if code != exitOK || !strings.Contains(out, "app-host") {
		t.Errorf("list after promote:\n%s", out)
	}
}

func TestPrintApplyReportTellsTheWholeStory(t *testing.T) {
	var buf bytes.Buffer
	res := apply.Result{
		Steps: []apply.StepResult{
			{Control: "update", Title: "system update", State: apply.StepDone},
			{Control: "mymo-user", Title: "mymo admin user", State: apply.StepKept},
			{Control: "sshd", Title: "sshd hardening", State: apply.StepFailed,
				Note: "the gate refused: key rejected; sshd was not reloaded — the operator's current access is untouched"},
			{Control: "docker", Title: "docker engine", State: apply.StepBlocked},
		},
		Failed:   true,
		FailedAt: "ssh-hardening",
	}
	printApplyReport(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"DONE    system update",
		"KEPT    mymo admin user",
		"FAILED  sshd hardening",
		"the gate refused",
		"BLOCKED docker engine",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the mymo key was proven") {
		t.Error("a failed gate reported a proven proof")
	}
}

func TestPrintVerifyChecksMarksFailures(t *testing.T) {
	var buf bytes.Buffer
	printVerifyChecks(&buf, []preflight.Check{
		{Group: "verify", Title: "sshd posture", Outcome: preflight.Pass, Detail: "the hardened drop-in is live"},
		{Group: "verify", Title: "firewall", Outcome: preflight.Abort, Detail: "ufw is inactive"},
	})
	out := buf.String()
	if !strings.Contains(out, "OK     sshd posture") || !strings.Contains(out, "FAILED firewall") {
		t.Errorf("verify rows:\n%s", out)
	}
}

var _ = context.Background

func TestInspectShowsTheRecordedBaseline(t *testing.T) {
	s := newSession(t)
	s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	n.Facts = facts.Node{
		Hostname: "web-1", OS: "Ubuntu 24.04 LTS", User: "root",
		CollectedAt:    time.Now(),
		BaselineMarker: `{"baseline": "0.1", "appliedAt": "2026-02-15T10:00:00Z"}`,
	}
	if err := store.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	code, out, _ := s.run(t, "node", "inspect", "web-1")
	if code != exitOK {
		t.Fatalf("inspect failed")
	}
	if !strings.Contains(out, "0.1 applied 2026-02-15T10:00:00Z") {
		t.Errorf("inspect missing the baseline row:\n%s", out)
	}
}

func TestInspectNamesDriftWhenTheVersionsDiffer(t *testing.T) {
	s := newSession(t)
	s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	n.Facts = facts.Node{
		Hostname: "web-1", OS: "Ubuntu 24.04 LTS", User: "root",
		CollectedAt:    time.Now(),
		BaselineMarker: `{"baseline": "0.0", "appliedAt": "2025-01-01T00:00:00Z"}`,
	}
	if err := store.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	_, out, _ := s.run(t, "node", "inspect", "web-1")
	if !strings.Contains(out, "0.0 recorded — mymo pins "+baseline.Version) {
		t.Errorf("inspect missing the drift row:\n%s", out)
	}
}
