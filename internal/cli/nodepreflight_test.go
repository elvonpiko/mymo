package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/preflight"
)

func TestPrintPreflightRendersVerdicts(t *testing.T) {
	checks := []preflight.Check{
		{Group: "platform", Title: "platform", Outcome: preflight.Pass, Detail: "Ubuntu 24.04.5 LTS · x86_64 · systemd — supported"},
		{Group: "ports", Title: "ports", Outcome: preflight.Decide, Detail: "80 nginx, 443 free — Caddy is mymo's only proxy"},
		{Group: "docker", Title: "docker", Outcome: preflight.Adopt, Detail: "official docker-ce — mymo can adopt and manage it"},
	}
	var buf bytes.Buffer
	printPreflight(&buf, "web-1", checks, preflight.Decide)
	out := buf.String()
	for _, want := range []string{
		"preflight · web-1 · baseline 0.1",
		"ok     platform", "Ubuntu 24.04.5 LTS",
		"decide ports", "80 nginx",
		"adopt  docker", "adopt and manage it",
		"verdict: decide", "your call is required",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestPrintPreflightAbortVerdict(t *testing.T) {
	var buf bytes.Buffer
	printPreflight(&buf, "web-1", []preflight.Check{
		{Group: "platform", Title: "platform", Outcome: preflight.Abort, Detail: "Alpine Linux v3.20 — baseline 0.1 supports Ubuntu"},
	}, preflight.Abort)
	if !strings.Contains(buf.String(), "cannot be prepared as an app host") {
		t.Errorf("abort verdict not stated:\n%s", buf.String())
	}
}

func TestRunNodePreflightUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runNodePreflight(context.Background(), nil, &out, &errOut); code != exitUsage {
		t.Errorf("no-args exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "usage: mymo node preflight") {
		t.Errorf("usage missing: %q", errOut.String())
	}
}
