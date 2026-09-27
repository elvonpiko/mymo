package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// cannedAudit answers a clean Ubuntu host: nothing installed, ports
// free, privilege held.
func cannedAudit() preflight.Audit {
	return preflight.Audit{
		UID: 1000, Sudo: true, Firewall: "absent",
		SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}},
		Listeners:     map[int]string{},
	}
}

func TestPreflightFromObservePage(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	// a clean host: nothing installed, so the audit finds a clear path
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)

	// p starts the audit under the loading ceremony
	m = press(t, m, "p")
	if m.cur().kind != scNodePreflight {
		t.Fatalf("p did not open the preflight screen: %v", m.cur().kind)
	}
	if !m.loading.active || !strings.Contains(m.loading.line, "auditing web-1") {
		t.Fatalf("audit did not start the ceremony: %+v", m.loading)
	}
	if out := view(m); !strings.Contains(out, "auditing web-1") || !strings.Contains(out, "cancel") {
		t.Fatal("loading page does not show the audit or how to cancel")
	}

	// the audit lands: loading clears, verdicts render, nothing was
	// promised beyond this phase
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: cannedAudit()})
	if m.loading.active {
		t.Fatal("audit finished but the ceremony never cleared")
	}
	out := view(m)
	for _, want := range []string{
		"Preflight", "baseline 0.1", "read-only audit",
		"platform", "Ubuntu 24.04.5 LTS", "privilege",
		"passwordless sudo as root", "free for Caddy",
		"verdict: ok", "the path is clear",
		"nothing on the node has changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preflight view missing %q\n%s", want, out)
		}
	}

	// the audit is recorded on the node: bootstrap sits at preflight
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.State != domain.BootstrapPreflight {
		t.Errorf("bootstrap state = %q, want preflight", n.Bootstrap.State)
	}
	if n.Bootstrap.Verdict != "ok" || n.Bootstrap.Baseline != "0.1" {
		t.Errorf("bootstrap record = %+v", n.Bootstrap)
	}

	// esc drops back to the observe page
	m = press(t, m, "esc")
	if m.cur().kind != scNode {
		t.Fatalf("esc did not leave the preflight screen: %v", m.cur().kind)
	}
}

func TestPreflightDecideShowsBlockedPath(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	m = observe(t, m, "web-1", snap)

	m = press(t, m, "p")
	audit := cannedAudit()
	audit.Listeners = map[int]string{80: "nginx"} // the classic conflict
	snap.Docker = ""
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: audit})

	out := view(m)
	for _, want := range []string{"80 nginx", "443 free", "only proxy", "verdict: decide"} {
		if !strings.Contains(out, want) {
			t.Errorf("decide verdict missing %q\n%s", want, out)
		}
	}

	// a decision node still records its verdict
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.Verdict != "decide" {
		t.Errorf("recorded verdict = %q, want decide", n.Bootstrap.Verdict)
	}
}

func TestPreflightFailureToastsAndReturns(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", err: errString("connection lost")})

	if m.cur().kind != scNode {
		t.Fatalf("failed audit did not return to the observe page: %v", m.cur().kind)
	}
	if m.loading.active {
		t.Fatal("failed audit left the ceremony running")
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "preflight failed") {
		t.Fatal("failed audit did not toast")
	}
}

func TestPreflightLeavingDuringAuditStillRecords(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	m = press(t, m, "p")
	m = press(t, m, "esc") // impatient user leaves mid-audit
	if m.cur().kind != scNode {
		t.Fatalf("esc did not cancel the audit visit: %v", m.cur().kind)
	}
	m = step(t, m, pfDoneMsg{node: "web-1", snap: richSnapshot("web-1"), audit: cannedAudit()})

	if m.cur().kind != scNode {
		t.Fatalf("audit landing must not re-open a closed screen: %v", m.cur().kind)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.State != domain.BootstrapPreflight {
		t.Errorf("a read-only audit must still record: state = %q", n.Bootstrap.State)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "preflight web-1") {
		t.Fatalf("the landed audit said nothing after the user left: %+v", m.toast)
	}
}

func TestPreflightNeverRevertsDeeperStates(t *testing.T) {
	// a node already confirmed for preparation keeps its position when
	// the audit is re-run
	s := readyStore(t)
	seedNode(t, s, "web-1")
	n, _ := s.GetNode("web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapConfirmed, Baseline: "0.1", At: time.Now()}
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}

	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", snap: richSnapshot("web-1"), audit: cannedAudit()})

	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.State != domain.BootstrapConfirmed {
		t.Errorf("re-running the audit rewound the lifecycle to %q", n.Bootstrap.State)
	}
}

func TestWrapPlain(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want []string
	}{
		{"short", 40, []string{"short"}},
		{"", 40, []string{""}},
		{"one two three four", 8, []string{"one two", "three", "four"}},
		{"unbreakableword", 8, []string{"unbreaka", "bleword"}}, // hard cut
	}
	for _, tc := range cases {
		got := wrapPlain(tc.in, tc.w)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapPlain(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}
