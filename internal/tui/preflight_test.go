package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/plan"
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
		"Initial setup", "the check", "read-only",
		"platform", "Ubuntu 24.04.5 LTS", "privilege",
		"passwordless sudo as root", "free for Caddy",
		"everything mymo needs is already here",
		"enter to see the plan",
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
	for _, want := range []string{"80 nginx", "443 free", "only proxy", "a few findings need your call"} {
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

func TestPlanFromClearedPreflight(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: cannedAudit()})

	// enter continues a cleared audit into the plan flow
	m = press(t, m, "enter")
	if m.cur().kind != scNodePlan {
		t.Fatalf("enter did not start the plan flow: %v", m.cur().kind)
	}
	if !m.loading.active || !strings.Contains(m.loading.line, "drafting web-1") {
		t.Fatalf("plan flow did not open the ceremony: %+v", m.loading)
	}
	out := view(m)
	if !strings.Contains(out, "drafting web-1") || !strings.Contains(out, "cancel") {
		t.Fatal("plan loading page missing")
	}

	// the plan lands: the change list renders, the plan step persists
	steps := []plan.Step{
		{Control: "mymo-user", Title: "mymo admin user", Detail: "create mymo user; install its key and sudoers drop-in"},
		{Control: "ssh-hardening", Title: "sshd hardening", Detail: "harden sshd: no root, no passwords, in a drop-in", Gate: "a second connection with the new mymo key is proven before sshd reloads"},
		{Control: "state-dir", Title: "mymo state", Detail: "state dir + baseline marker for drift detection"},
	}
	m = step(t, m, planMsg{node: "web-1", steps: steps})
	if m.loading.active {
		t.Fatal("plan landed but the ceremony never cleared")
	}
	out = ansiStrip(view(m))
	for _, want := range []string{
		"Initial setup", "the plan", "web-1", "baseline 0.1", "3 steps",
		"mymo admin user", "sshd hardening", "mymo state",
		"second connection",
		"type web-1 in full to apply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan view missing %q\n%s", want, out)
		}
	}

	// the node records the plan step
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.State != domain.BootstrapPlan {
		t.Errorf("bootstrap state = %q, want plan", n.Bootstrap.State)
	}

	// esc returns to the audit's verdicts
	m = press(t, m, "esc")
	if m.cur().kind != scNodePreflight {
		t.Fatalf("esc did not return to the preflight screen: %v", m.cur().kind)
	}
}

func TestPlanBlockedByDecisionsToasts(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	audit := cannedAudit()
	audit.Listeners = map[int]string{80: "nginx"}
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: audit})

	// enter on a blocked verdict does not enter the plan flow
	m = press(t, m, "enter")
	if m.cur().kind != scNodePreflight {
		t.Fatalf("enter started planning past decisions: %v", m.cur().kind)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "resolve the decisions") {
		t.Fatalf("blocked enter must toast: %+v", m.toast)
	}
}

func TestPlanRefusalKeepsLifecycleAndToasts(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: cannedAudit()})
	m = press(t, m, "enter")

	// generation fails mid-flow: the screen pops, nothing advances
	m = step(t, m, planMsg{node: "web-1", err: errString("connection lost")})
	if m.cur().kind != scNodePreflight {
		t.Fatalf("failed plan did not return to the audit: %v", m.cur().kind)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Bootstrap.State != domain.BootstrapPreflight {
		t.Errorf("a failed plan advanced the lifecycle to %q", n.Bootstrap.State)
	}
}

// ansiStrip removes color sequences so assertions can reason about
// plain text.
func ansiStrip(s string) string {
	re := regexp.MustCompile("\x1b" + "\\[[0-9;]*m")
	return re.ReplaceAllString(s, "")
}

// leadSpaces reports the gutter width of a framed content row: the
// spaces between the frame border and the row's first visible cell.
func leadSpaces(line string) int {
	plain := ansiStrip(line)
	// framed rows start at the border; the header row floats free
	plain = strings.TrimPrefix(plain, "\u2502")
	return len(plain) - len(strings.TrimLeft(plain, " "))
}

func TestWideTerminalCentersTheStage(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := New(s)
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = press(t, m, "n")
	m = press(t, m, "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	lines := strings.Split(view(m), "\n")
	// the identity line floats center
	if got := leadSpaces(lines[1]); got < 10 {
		t.Errorf("header not centered at 120 cols: gutter=%d", got)
	}
	// the page's own rows share one gutter: heading above, system card
	// below — a centered block, not line-by-line scattering
	heading, card, actions := leadSpaces(lines[3]), leadSpaces(lines[5]), leadSpaces(lines[17])
	if heading == 0 || heading != card || heading != actions {
		t.Errorf("stage not one centered block: heading=%d card=%d actions=%d", heading, card, actions)
	}
	// the footer centers with it
	if got := leadSpaces(lines[28]); got < 10 {
		t.Errorf("footer not centered at 120 cols: gutter=%d", got)
	}
}

func TestToastRidesTheFooterRow(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	audit := cannedAudit()
	audit.Listeners = map[int]string{80: "nginx"}
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: audit})
	m = press(t, m, "enter")

	lines := strings.Split(view(m), "\n")
	// the notice takes the footer row, centered — never the header
	footer := lines[len(lines)-2]
	if !strings.Contains(footer, "resolve the decisions above, then plan") {
		t.Errorf("toast not in the footer row: %q", footer)
	}
	if leadSpaces(footer) < 8 {
		t.Errorf("toast not centered: %q", footer)
	}
	if strings.Contains(lines[1], "resolve the decisions") {
		t.Errorf("toast leaked into the header: %q", lines[1])
	}
	// a blocked verdict drops the plan key from the footer advice
	if strings.Contains(footer, "enter plan") {
		t.Errorf("footer offers planning past a decision: %q", footer)
	}
}

func TestAbortVerdictToastsTheTruth(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	audit := cannedAudit()
	audit.UID = 1000
	audit.Sudo = false
	audit.Firewall = "unknown"
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: audit})

	m = press(t, m, "enter")
	if m.cur().kind != scNodePreflight {
		t.Fatalf("enter started planning past an abort: %v", m.cur().kind)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "cannot be prepared") {
		t.Fatalf("abort must say so, not talk of decisions: %+v", m.toast)
	}
	if m.toast.kind != toastErr {
		t.Errorf("an abort notice is an error, got kind %d", m.toast.kind)
	}
	lines := strings.Split(view(m), "\n")
	if strings.Contains(lines[len(lines)-2], "enter plan") {
		t.Errorf("footer offers planning past an abort: %q", lines[len(lines)-2])
	}
}
