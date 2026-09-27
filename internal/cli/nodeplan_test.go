package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
)

func planSteps() []plan.Step {
	return []plan.Step{
		{
			Control: "ssh-hardening", Title: "sshd hardening",
			Detail: "harden sshd: no root, no passwords, in a drop-in",
			Files: []plan.File{
				{Path: "/etc/ssh/sshd_config.d/60-mymo.conf", Content: "PermitRootLogin no\n", Mode: "0644", Owner: "root:root"},
			},
			Exec: [][]string{{"sshd", "-t"}},
			Gate: "a second connection with the new mymo key is proven before sshd reloads",
		},
		{
			Control: "docker", Title: "docker engine",
			Detail:   "install docker-ce + plugins from the official apt repo",
			Packages: []string{"docker-ce", "docker-ce-cli", "containerd.io"},
		},
	}
}

func TestPrintPlanRendersFullChangeList(t *testing.T) {
	var buf bytes.Buffer
	printPlan(&buf, "web-1", planSteps())
	out := buf.String()
	for _, want := range []string{
		"plan \u00b7 web-1 \u00b7 baseline 0.1 \u00b7 2 steps",
		"1. sshd hardening",
		"writes /etc/ssh/sshd_config.d/60-mymo.conf (0644, root:root)",
		"| PermitRootLogin no",
		"runs sshd -t",
		"gate: a second connection",
		"2. docker engine",
		"installs docker-ce, docker-ce-cli, containerd.io",
		"this plan changes nothing until it is confirmed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q\n%s", want, out)
		}
	}
}

func TestPrintPlanBlockedListsDecisions(t *testing.T) {
	var buf bytes.Buffer
	blocked := &plan.BlockedError{Decisions: []preflight.Check{
		{Group: "ports", Title: "ports", Outcome: preflight.Decide, Detail: "80 nginx, 443 free \u2014 free the ports first"},
		{Group: "docker", Title: "docker", Outcome: preflight.Decide, Detail: "distribution docker.io installed"},
	}}
	printPlanBlocked(&buf, blocked)
	out := buf.String()
	for _, want := range []string{
		"planning is blocked",
		"ports", "80 nginx",
		"docker", "docker.io installed",
		"mymo plans nothing past a decision",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("blocked output missing %q\n%s", want, out)
		}
	}
}

func TestRunNodePlanUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runNodePlan(context.Background(), nil, &out, &errOut); code != exitUsage {
		t.Errorf("no-args exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "usage: mymo node plan") {
		t.Errorf("usage missing: %q", errOut.String())
	}
}
