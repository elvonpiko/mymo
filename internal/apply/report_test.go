package apply

import (
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/preflight"
)

// The finished page speaks once: one row per control, the live
// probe folded into the step it proves, and no second list telling
// a different story underneath.
func TestBuildReportFoldsLiveProbesIntoSteps(t *testing.T) {
	res := Result{Steps: []StepResult{
		{Control: "mymo-user", Title: "mymo admin user", State: StepKept, Detail: "create mymo user with its key and sudo rule"},
		{Control: "ssh-hardening", Title: "sshd hardening", State: StepDone, Detail: "no root ssh; access continues as mymo + console"},
		{Control: "sysctl", Title: "kernel hardening", State: StepDone, Detail: "pin the tested sysctl floor via /etc/sysctl.d"},
	}}
	checks := []preflight.Check{
		{Group: "verify", Title: "sshd posture", Outcome: preflight.Pass, Detail: "no root, no passwords — the live posture"},
		{Group: "verify", Title: "mymo user", Outcome: preflight.Pass, Detail: "the mymo admin user exists"},
		{Group: "verify", Title: "mymo key access", Outcome: preflight.Pass, Detail: "the key works on a fresh connection"},
	}
	rows := BuildReport(res, checks)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want one per step", len(rows))
	}
	byControl := map[string]Row{}
	for _, r := range rows {
		byControl[r.Control] = r
	}
	// a probed step reads OK with its live descriptions — the step
	// titles stay, the probe's own title never shows twice
	if r := byControl["ssh-hardening"]; r.Status != "OK" {
		t.Errorf("ssh hardening = %s, want OK (live probe passed)", r.Status)
	} else if len(r.Detail) != 1 || !strings.Contains(r.Detail[0], "live posture") {
		t.Errorf("ssh hardening detail = %v, want the live description", r.Detail)
	}
	if r := byControl["mymo-user"]; r.Status != "OK" || len(r.Detail) != 2 {
		t.Errorf("mymo user = %s/%v, want OK with both its proofs", r.Status, r.Detail)
	}
	// a step with no live probe reads DONE with its own description
	if r := byControl["sysctl"]; r.Status != "DONE" || !strings.Contains(strings.Join(r.Detail, " "), "sysctl floor") {
		t.Errorf("sysctl = %s/%v, want DONE with its description kept", r.Status, r.Detail)
	}
}

// The box's word for itself outranks mymo's bookkeeping: a step that
// ran fine but does not hold in effective state reads FAILED with
// the probe's reason — never DONE, never "ok" beside it.
func TestLiveTruthWinsOverTheApplyClaim(t *testing.T) {
	res := Result{Steps: []StepResult{
		{Control: "ssh-hardening", Title: "sshd hardening", State: StepDone, Detail: "no root ssh"},
		{Control: "firewall", Title: "firewall", State: StepDone, Detail: "install and enable ufw"},
	}}
	checks := []preflight.Check{
		{Group: "verify", Title: "sshd posture", Outcome: preflight.Abort,
			Detail: `effective PasswordAuthentication is "yes" — the baseline expects "no"`},
		{Group: "verify", Title: "firewall", Outcome: preflight.Pass, Detail: "ufw is active"},
	}
	rows := BuildReport(res, checks)
	for _, r := range rows {
		switch r.Control {
		case "ssh-hardening":
			if r.Status != "FAILED" || !strings.Contains(strings.Join(r.Detail, "; "), "PasswordAuthentication") {
				t.Errorf("sshd = %s/%v, want FAILED naming the lying directive", r.Status, r.Detail)
			}
		case "firewall":
			if r.Status != "OK" {
				t.Errorf("firewall = %s, want OK", r.Status)
			}
		}
	}
}

// A stopped apply is the whole story: the failure's reason, the
// blocked tail, no probes to fold in.
func TestBuildReportOnAFailedApply(t *testing.T) {
	res := Result{Steps: []StepResult{
		{Control: "ssh-hardening", Title: "sshd hardening", State: StepFailed,
			Note: "the gate refused: key rejected; sshd was not reloaded", Detail: "no root ssh"},
		{Control: "caddy", Title: "caddy", State: StepBlocked, Detail: "install caddy"},
	}}
	rows := BuildReport(res, nil)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per step", len(rows))
	}
	if rows[0].Status != "FAILED" || !strings.Contains(rows[0].Detail[0], "gate refused") {
		t.Errorf("failed step row = %s/%v", rows[0].Status, rows[0].Detail)
	}
	if rows[1].Status != "BLOCKED" || len(rows[1].Detail) != 0 {
		t.Errorf("blocked step row = %s/%v, want BLOCKED with nothing claimed", rows[1].Status, rows[1].Detail)
	}
}

// A probe nobody maps is never silently dropped: it gets its own row.
func TestAnUnmappedProbeGetsItsOwnRow(t *testing.T) {
	res := Result{Steps: []StepResult{
		{Control: "caddy", Title: "caddy", State: StepDone, Detail: "install caddy"},
	}}
	checks := []preflight.Check{
		{Group: "verify", Title: "caddy", Outcome: preflight.Pass, Detail: "installed and enabled"},
		{Group: "verify", Title: "something new", Outcome: preflight.Abort, Detail: "a probe from a newer mymo"},
	}
	rows := BuildReport(res, checks)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the step and the unmapped probe", len(rows))
	}
	if rows[1].Title != "something new" || rows[1].Status != "FAILED" {
		t.Errorf("unmapped probe row = %+v", rows[1])
	}
}

// A mapped probe whose step is not in the result — an older plan, a
// newer probe — keeps its own row: mapping must fold, never swallow.
func TestAMappedProbeWithoutItsStepKeepsItsRow(t *testing.T) {
	res := Result{Steps: []StepResult{
		{Control: "caddy", Title: "caddy", State: StepDone, Detail: "install caddy"},
	}}
	checks := []preflight.Check{
		{Group: "verify", Title: "firewall", Outcome: preflight.Abort, Detail: "ufw is inactive"},
	}
	rows := BuildReport(res, checks)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the step and the orphaned probe", len(rows))
	}
	if rows[1].Title != "firewall" || rows[1].Status != "FAILED" || !strings.Contains(rows[1].Detail[0], "inactive") {
		t.Errorf("orphaned probe row = %+v", rows[1])
	}
}
