package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
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

func TestNodeScreenShowsDiscoveredFacts(t *testing.T) {
	s := readyStore(t)
	nodeWithFacts(t, s, "web-1")
	got := view(press(t, New(s), "n", "enter"))
	for _, want := range []string{
		"Ubuntu 24.04.5 LTS", "x86_64", "6.8.0-31-generic",
		"2", "1 day 18 hours", "3.2 GiB/3.8 GiB avail", "32 GiB/39 GiB free",
		"27.3.1", "not installed", "checked just now",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("node screen missing %q:\n%s", want, got)
		}
	}
}

func TestNodeScreenUncheckedHint(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	got := view(press(t, New(s), "n", "enter"))
	for _, want := range []string{"no snapshot yet", "check now", "read-only commands", "unchecked"} {
		if !strings.Contains(got, want) {
			t.Errorf("unchecked node missing %q:\n%s", want, got)
		}
	}
}

func TestNodeScreenFailedCheckKeepsFacts(t *testing.T) {
	s := readyStore(t)
	nodeWithFacts(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	mm := step(t, m, checkDoneMsg{node: "web-1", err: errString("unreachable: connection refused")})

	if mm.probing {
		t.Fatal("probe state not cleared")
	}
	if mm.toast == nil || !strings.Contains(mm.toast.text, "check failed") {
		t.Fatalf("no failure toast: %+v", mm.toast)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.LastCheck.Error == "" {
		t.Fatal("failure not recorded in LastCheck")
	}
	// the last good snapshot must survive a failed attempt
	if n.Facts.OS != "Ubuntu 24.04.5 LTS" || n.Facts.Docker == "" {
		t.Fatalf("facts clobbered by failed check: %+v", n.Facts)
	}
	got := view(mm)
	if !strings.Contains(got, "Ubuntu 24.04.5 LTS") {
		t.Errorf("failed-check node screen lost its facts:\n%s", got)
	}
	if !strings.Contains(got, "last check failed") {
		t.Errorf("failed-check node screen missing the failure:\n%s", got)
	}
}

func TestCheckKeyStartsProbe(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter", "c")
	if !m.probing || m.probingName != "web-1" {
		t.Fatalf("c did not start a probe: probing=%v name=%q", m.probing, m.probingName)
	}
	got := view(m)
	if !strings.Contains(got, "checking over SSH") {
		t.Errorf("probing node screen missing spinner state:\n%s", got)
	}

	// a second c while probing is refused with a toast, not silently
	m = press(t, m, "c")
	if m.toast == nil || !strings.Contains(m.toast.text, "already checking web-1") {
		t.Fatalf("no already-checking toast: %+v", m.toast)
	}
}

func TestCheckDoneUpdatesEverything(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter", "c")
	snap := facts.Node{
		Hostname: "web-1", OS: "Debian GNU/Linux 12", Docker: "Docker version 24.0.7, build x",
		CollectedAt: time.Now(),
	}
	m2, _ := m.Update(checkDoneMsg{node: "web-1", snap: snap})
	mm := m2.(Model)

	if mm.probing {
		t.Fatal("probe state not cleared")
	}
	if mm.toast == nil || !strings.Contains(mm.toast.text, "checked web-1") {
		t.Fatalf("no success toast: %+v", mm.toast)
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Facts.OS != "Debian GNU/Linux 12" {
		t.Fatalf("snapshot not persisted: %+v", n.Facts)
	}
	if n.LastCheck.Error != "" || n.LastCheck.At.IsZero() {
		t.Fatalf("LastCheck not recorded: %+v", n.LastCheck)
	}
	got := view(mm)
	if !strings.Contains(got, "Debian GNU/Linux 12") {
		t.Errorf("node screen not refreshed with new facts:\n%s", got)
	}
	if strings.Contains(got, "checking over SSH") {
		t.Errorf("spinner state leaked after completion:\n%s", got)
	}
}

func TestSSHKeyToastsWithoutBinary(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	// an empty PATH: no ssh client to find
	t.Setenv("PATH", t.TempDir())
	m := press(t, New(s), "n", "enter", "s")
	if m.toast == nil || !strings.Contains(m.toast.text, "ssh") {
		t.Fatalf("no missing-binary toast: %+v", m.toast)
	}
}

func TestInspectShowsRecordAndFacts(t *testing.T) {
	s := readyStore(t)
	nodeWithFacts(t, s, "web-1")
	// three downs land on Inspect (fourth action)
	got := view(press(t, New(s), "n", "enter", "down", "down", "down", "enter"))
	for _, want := range []string{
		"Stored record", "Discovered", "Ubuntu 24.04.5 LTS", "just now", "last check", "ok just now",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect missing %q:\n%s", want, got)
		}
	}
}

// errString is a tiny error type for simulating probe failures.
type errString string

func (e errString) Error() string { return string(e) }
