package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/apply"
	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// cannedApplyRunner answers the engine and the verifier the way a
// prepared node would. failCmd names one command that fails.
type cannedApplyRunner struct {
	failCmd string
	calls   []string
}

func (r *cannedApplyRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	argv := append([]string{name}, args...)
	joined := strings.Join(argv, " ")
	r.calls = append(r.calls, joined)
	if name == r.failCmd {
		return "boom", 1, fmt.Errorf("exit 1")
	}
	switch {
	case name == "sshd" && len(args) > 0 && args[0] == "-T":
		var b strings.Builder
		for _, d := range baseline.SSHDirectives {
			fmt.Fprintf(&b, "%s %s\n", strings.ToLower(d.Key), d.Value)
		}
		return b.String(), 0, nil
	case name == "ufw":
		return "Status: active\n", 0, nil
	case name == "id":
		return "uid=1000(mymo)", 0, nil
	case name == "cat" && strings.HasSuffix(joined, "baseline.json"):
		return "{\"baseline\": \"0.1\", \"appliedAt\": \"2026-02-15T10:00:00Z\"}\n", 0, nil
	case name == "systemctl" && len(args) > 1 && args[0] == "is-enabled":
		return "enabled", 0, nil
	}
	return "", 0, nil
}

// fakeTUIProver is the injectable second connection.
type fakeTUIProver struct{ err error }

func (p fakeTUIProver) ProveMymoKey(context.Context) error { return p.err }

// drive feeds one message and then runs the returned commands the
// way bubbletea's loop would, collecting the messages they produce —
// the apply flow returns real commands that step() would discard.
func drive(t *testing.T, m Model, msg tea.Msg) (Model, []tea.Msg) {
	t.Helper()
	next, cmd := m.Update(msg)
	mm := next.(Model)
	var msgs []tea.Msg
	queue := []tea.Cmd{}
	if cmd != nil {
		queue = append(queue, cmd)
	}
	for i := 0; len(queue) > 0 && i < 64; i++ {
		c := queue[0]
		queue = queue[1:]
		res, ok := runCmdTimed(c, 50*time.Millisecond)
		if !ok {
			continue // the spinner's real-time tick
		}
		switch r := res.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, r...)
		default:
			msgs = append(msgs, r)
		}
	}
	return mm, msgs
}

// drain feeds every produced message back the way bubbletea's loop
// would, following returned commands until the flow rests.
func drain(t *testing.T, m Model, msgs []tea.Msg) Model {
	t.Helper()
	for i := 0; len(msgs) > 0 && i < 128; i++ {
		var next []tea.Msg
		for _, msg := range msgs {
			var produced []tea.Msg
			m, produced = drive(t, m, msg)
			next = append(next, produced...)
		}
		msgs = next
	}
	return m
}

// atPlanScreen drives a node through a cleared preflight into its
// drafted plan, with the apply's runner and prover injected.
func atPlanScreen(t *testing.T, runner preflight.Runner, prover apply.Prover) Model {
	t.Helper()
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := New(s)
	m.applyRunner = runner
	m.newProver = func(string) apply.Prover { return prover }
	m = press(t, m, "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	snap.Caddy = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: cannedAudit()})
	m = press(t, m, "enter")
	m = step(t, m, planMsg{node: "web-1", steps: testPlanSteps()})
	if m.cur().kind != scNodePlan {
		t.Fatalf("not on the plan screen: %v", m.cur().kind)
	}
	return m
}

// testPlanSteps mirrors the shape the real generator produces for a
// cleared fresh-node preflight — the engine runs these over the
// canned runner exactly as it would over SSH.
func testPlanSteps() []plan.Step {
	return []plan.Step{
		{Control: "mymo-user", Title: "mymo admin user",
			Detail: "create mymo user; install its key and sudoers drop-in",
			Files: []plan.File{
				{Path: "/etc/sudoers.d/mymo", Content: "mymo ALL=(ALL) NOPASSWD:ALL\n", Mode: "0440", Owner: "root:root"},
			},
			Exec: [][]string{
				{"useradd", "-m", "-s", "/bin/bash", "mymo"},
				{"sh", "-c", "printf '%s\n' '<mymo-public-key>' > /home/mymo/.ssh/authorized_keys"},
				{"visudo", "-cf", "/etc/sudoers.d/mymo"},
			}},
		{Control: "ssh-hardening", Title: "sshd hardening",
			Detail: "harden sshd: no root, no passwords, in a drop-in",
			Files: []plan.File{
				{Path: "/etc/ssh/sshd_config.d/60-mymo.conf", Content: "PermitRootLogin no\nPasswordAuthentication no\n", Mode: "0644", Owner: "root:root"},
			},
			Exec: [][]string{
				{"sshd", "-t"},
				{"systemctl", "reload", "ssh"},
			},
			Gate: "a second connection with the new mymo key is proven before sshd reloads"},
		{Control: "state-dir", Title: "mymo state",
			Detail: "state dir + baseline marker for drift detection",
			Files: []plan.File{
				{Path: "/var/lib/mymo/baseline.json", Content: "{\n  \"baseline\": \"0.1\",\n  \"appliedAt\": \"<at apply>\"\n}\n", Mode: "0644", Owner: "mymo:mymo"},
			},
			Exec: [][]string{
				{"install", "-d", "-m", "0750", "-o", "mymo", "-g", "mymo", "/var/lib/mymo"},
			}},
	}
}

func TestApplyWantsTheExactName(t *testing.T) {
	runner := &cannedApplyRunner{}
	m := atPlanScreen(t, runner, fakeTUIProver{})

	// the plan page is the confirmation: the steps, the guard, and
	// the typed name on one card
	if m.cur().kind != scNodePlan {
		t.Fatalf("not on the plan page: %v", m.cur().kind)
	}
	out := view(m)
	for _, want := range []string{"Initial setup", "type web-1 in full to apply",
		"a second connection with the new mymo key"} {
		if !strings.Contains(ansiStrip(out), want) {
			t.Errorf("plan view missing %q:\n%s", want, out)
		}
	}

	// a near-miss is refused, loudly
	m = press(t, m, "w", "e", "b", "enter")
	if m.cur().kind != scNodePlan {
		t.Fatalf("a wrong name left the plan page: %v", m.cur().kind)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "does not match") {
		t.Fatal("the wrong name did not toast")
	}

	// esc goes back to the check page without a trace
	mEsc := atPlanScreen(t, runner, fakeTUIProver{})
	mEsc = press(t, mEsc, "esc")
	if mEsc.cur().kind != scNodePreflight {
		t.Fatalf("esc did not go back to the check: %v", mEsc.cur().kind)
	}

	// the exact name proceeds: confirmed, then applying
	m = press(t, m, "w", "e", "b", "-", "1", "enter")
	if m.cur().kind != scNodeApplyReport {
		t.Fatalf("the matching name did not start the apply: %v", m.cur().kind)
	}
	if !m.loading.active {
		t.Fatal("the apply ceremony is not running")
	}
	n, err := m.store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Bootstrap.State != domain.BootstrapApplying || n.Bootstrap.Verdict != "applying" {
		t.Fatalf("bootstrap = %s/%s, want applying", n.Bootstrap.State, n.Bootstrap.Verdict)
	}
	// the apply borrowed the live connection
	if m.live || !m.livePaused || m.liveClient == nil {
		t.Fatalf("apply did not pause sampling: live=%v paused=%v", m.live, m.livePaused)
	}
}

func TestApplyFailureStopsAndReportsWhere(t *testing.T) {
	runner := &cannedApplyRunner{failCmd: "visudo"}
	m := atPlanScreen(t, runner, fakeTUIProver{})

	m = press(t, m, "w", "e", "b", "-", "1")
	m, msgs := drive(t, m, keyMsg("enter"))
	m = drain(t, m, msgs)
	if m.cur().kind != scNodeApplyReport {
		t.Fatalf("the failure should stay on the report: %v", m.cur().kind)
	}
	out := ansiStrip(view(m))
	for _, want := range []string{
		"FAILED", "BLOCKED", "the steps after the failure were never attempted",
		"untouched",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	n, err := m.store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	// a failed apply reopens the flow: the record rolls back to
	// preflight with the resume guidance — never stranded at
	// "applying" with no way back in
	if n.Bootstrap.State != domain.BootstrapPreflight || !strings.Contains(n.Bootstrap.Verdict, "preflight again to resume") {
		t.Fatalf("bootstrap = %s/%s, want preflight with the resume guidance", n.Bootstrap.State, n.Bootstrap.Verdict)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "systemctl reload ssh") {
		t.Fatal("a failed earlier step still reached the sshd reload")
	}

	// walking home resumes sampling over the kept connection
	m = press(t, m, "esc")
	if m.cur().kind != scNode {
		t.Fatalf("esc did not return to the observe page: %v", m.cur().kind)
	}
	if !m.live {
		t.Fatal("sampling did not resume after walking away from a failed apply")
	}
}

func TestApplyVerifiesAndReachesReady(t *testing.T) {
	runner := &cannedApplyRunner{}
	m := atPlanScreen(t, runner, fakeTUIProver{})

	m = press(t, m, "w", "e", "b", "-", "1")
	m, msgs := drive(t, m, keyMsg("enter"))
	// success flows straight into verify; the drain follows it
	m = drain(t, m, msgs)
	if m.cur().kind != scNodeApplyReport {
		t.Fatalf("should rest on the report: %v", m.cur().kind)
	}
	out := ansiStrip(view(m))
	for _, want := range []string{
		"DONE",
		"sshd posture", "applied and verified",
		"mymo node promote web-1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("verified report missing %q:\n%s", want, out)
		}
	}
	n, err := m.store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Bootstrap.State != domain.BootstrapReady || n.Bootstrap.Verdict != "ready" {
		t.Fatalf("bootstrap = %s/%s, want ready", n.Bootstrap.State, n.Bootstrap.Verdict)
	}
	if n.Mode != domain.ModeObserve {
		t.Fatal("a verified node was implicitly promoted — promotion must stay explicit")
	}
}
