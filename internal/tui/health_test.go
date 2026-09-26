package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

func TestClassifyNode(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		node domain.Node
		want healthClass
	}{
		{"never probed", domain.Node{}, hUnchecked},
		{"checked just now", domain.Node{LastCheck: domain.CheckState{At: now}}, hFresh},
		{"checked yesterday", domain.Node{LastCheck: domain.CheckState{At: now.Add(-30 * time.Hour)}}, hStale},
		{"failed recently", domain.Node{LastCheck: domain.CheckState{At: now.Add(-time.Hour), Error: "unreachable"}}, hFailed},
		{"failed long ago", domain.Node{LastCheck: domain.CheckState{At: now.Add(-30 * 24 * time.Hour), Error: "auth"}}, hFailed},
	}
	for _, tc := range cases {
		if got := classifyNode(tc.node); got != tc.want {
			t.Errorf("%s: classifyNode() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFleetHealthLabels(t *testing.T) {
	s := readyStore(t)
	n := seedNode(t, s, "web-1")
	n.LastCheck = domain.CheckState{At: time.Now()}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	got := view(press(t, New(s), "n"))
	if !strings.Contains(got, "checked just now") {
		t.Errorf("fleet missing fresh-check label:\n%s", got)
	}

	n.LastCheck = domain.CheckState{At: time.Now().Add(-48 * time.Hour), Error: "unreachable: refused"}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	got = view(press(t, New(s), "n"))
	if !strings.Contains(got, "failed 2d ago") {
		t.Errorf("fleet missing failure label:\n%s", got)
	}
}

func nodeWithFacts(t *testing.T, s *state.Store, name string) domain.Node {
	t.Helper()
	n := seedNode(t, s, name)
	n.Facts = facts.Node{
		Hostname:    name,
		OS:          "Ubuntu 24.04.5 LTS",
		Kernel:      "6.8.0-31-generic",
		Arch:        "x86_64",
		CPUs:        2,
		Uptime:      42 * time.Hour,
		MemTotal:    4106280960,
		MemAvail:    3445256192,
		DiskTotal:   42024214528,
		DiskFree:    34596003840,
		Docker:      "Docker version 27.3.1, build abc",
		Systemd:     true,
		User:        "root",
		CollectedAt: time.Now(),
	}
	n.LastCheck = domain.CheckState{At: time.Now()}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	return n
}

// fakeClient is an undialed client standing in for the probe's open
// connection; Close is safe on it.
func fakeClient() *ssh.Client { return ssh.New(domain.Node{}, "") }

// observe settles a successful entry probe for the named node, as if
// the transport had answered: the observe page is open and live.
func observe(t *testing.T, m Model, name string, snap facts.Node) Model {
	t.Helper()
	return step(t, m, checkDoneMsg{node: name, snap: snap, client: fakeClient(), seq: m.checkSeq})
}

func richSnapshot(name string) facts.Node {
	return facts.Node{
		Hostname: name, OS: "Ubuntu 24.04.5 LTS", Kernel: "6.8.0-31-generic",
		Arch: "x86_64", CPUs: 2, Uptime: 42 * time.Hour,
		MemTotal: 4106280960, MemAvail: 3445256192,
		DiskTotal: 42024214528, DiskFree: 34596003840,
		Docker: "Docker version 27.3.1, build abc", Systemd: true, User: "root",
		CollectedAt: time.Now(),
	}
}

func TestEnteringObservesWithLoadingPage(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")

	if !m.probing || m.probingName != "web-1" {
		t.Fatalf("enter did not start a probe: probing=%v name=%q", m.probing, m.probingName)
	}
	got := view(m)
	for _, want := range []string{"mymo", "observing web-1 over SSH", "ten read-only commands", "cancel"} {
		if !strings.Contains(got, want) {
			t.Errorf("loading page missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ACTIONS") {
		t.Errorf("loading page showed the observe page early:\n%s", got)
	}

	// esc cancels the visit: back on the fleet, probe flag cleared
	m = press(t, m, "esc")
	if m.probing || m.cur().kind != scFleet {
		t.Fatalf("esc did not cancel: probing=%v screen=%v", m.probing, m.cur().kind)
	}
}

func TestObservePageShowsFactsAndLive(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	got := view(m)
	for _, want := range []string{
		"Ubuntu 24.04.5 LTS", "x86_64", "6.8.0-31-generic", "1 day 18 hours",
		"32 GiB/39 GiB free", "27.3.1", "not installed", "checked just now",
		"● live", "3.2 GiB/3.8 GiB avail", "warming", "load",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("observe page missing %q:\n%s", want, got)
		}
	}
	// memory and cpu live in the live line only — never twice
	if strings.Contains(got, "memory") {
		t.Errorf("observe page still shows a captured memory row:\n%s", got)
	}
	// the page itself is the feedback: no extra toast while on it
	if m.toast != nil {
		t.Fatalf("unexpected toast on the observe page: %+v", m.toast)
	}
	// the probe's connection became the live session
	if !m.live || m.liveClient == nil {
		t.Fatal("probe connection not adopted for live stats")
	}
	if m.liveCur.memAvail != 3445256192 || m.liveCur.memTotal != 4106280960 {
		t.Fatalf("live memory not seeded from the snapshot: %+v", m.liveCur)
	}
}

func TestObservePageAfterFailedFirstCheck(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = step(t, m, checkDoneMsg{node: "web-1", err: errString("unreachable: connection refused"), seq: m.checkSeq})

	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.LastCheck.Error == "" {
		t.Fatal("failure not recorded in LastCheck")
	}
	got := view(m)
	for _, want := range []string{"first check failed", "connection refused", "re-enter to retry"} {
		if !strings.Contains(got, want) {
			t.Errorf("failed-first-check page missing %q:\n%s", want, got)
		}
	}
	if m.live || m.liveClient != nil {
		t.Fatal("failed probe must not leave a live session")
	}
}

func TestFailedCheckKeepsFactsAndStopsLive(t *testing.T) {
	s := readyStore(t)
	nodeWithFacts(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = step(t, m, checkDoneMsg{node: "web-1", err: errString("unreachable: connection refused"), seq: m.checkSeq})

	if m.probing {
		t.Fatal("probe state not cleared")
	}
	// staying on the page: the card is the feedback, no toast
	if m.toast != nil {
		t.Fatalf("unexpected toast on the observe page: %+v", m.toast)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	// the last good snapshot must survive a failed attempt
	if n.Facts.OS != "Ubuntu 24.04.5 LTS" || n.Facts.Docker == "" {
		t.Fatalf("facts clobbered by failed check: %+v", n.Facts)
	}
	got := view(m)
	if !strings.Contains(got, "Ubuntu 24.04.5 LTS") {
		t.Errorf("failed-check page lost its facts:\n%s", got)
	}
	if !strings.Contains(got, "last check failed") {
		t.Errorf("failed-check page missing the failure:\n%s", got)
	}
	// the live line degrades honestly, marking the last known memory
	// as a snapshot instead of pretending to be live
	if !strings.Contains(got, "live stopped") || !strings.Contains(got, "(snapshot)") {
		t.Errorf("stopped live line not shown:\n%s", got)
	}
}

func TestProbeResultRecordsAndToastsAfterLeaving(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	// leave while the probe is still running
	m = press(t, m, "esc")
	if m.cur().kind != scFleet {
		t.Fatal("esc did not return to the fleet")
	}
	// the result arrives late: still recorded, still toasted
	m = step(t, m, checkDoneMsg{
		node: "web-1", snap: richSnapshot("web-1"),
		client: fakeClient(), seq: m.checkSeq,
	})
	if m.toast == nil || !strings.Contains(m.toast.text, "checked web-1") {
		t.Fatalf("no toast for the missed result: %+v", m.toast)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Facts.OS != "Ubuntu 24.04.5 LTS" || n.LastCheck.At.IsZero() {
		t.Fatalf("late result not recorded: %+v %+v", n.Facts, n.LastCheck)
	}
	// and the unused connection was closed, not adopted
	if m.liveClient != nil {
		t.Fatal("connection adopted while off the node page")
	}
}

func TestSSHKeyToastsWithoutBinary(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	// an empty PATH: no ssh client to find. The key only exists once
	// the probe has settled and the observe page is up.
	t.Setenv("PATH", t.TempDir())
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = press(t, m, "s")
	if m.toast == nil || !strings.Contains(m.toast.text, "ssh") {
		t.Fatalf("no missing-binary toast: %+v", m.toast)
	}
}

func TestInspectShowsRecordAndFacts(t *testing.T) {
	s := readyStore(t)
	nodeWithFacts(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	// two downs land on Inspect record (third action)
	m = press(t, m, "down", "down", "enter")
	got := view(m)
	for _, want := range []string{
		"Stored record", "Discovered", "Ubuntu 24.04.5 LTS", "just now", "last check", "ok just now",
		"3.2 GiB/3.8 GiB avail", // the inspect record keeps memory
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect missing %q:\n%s", want, got)
		}
	}
}

// errString is a tiny error type for simulating probe failures.
type errString string

func (e errString) Error() string { return string(e) }
