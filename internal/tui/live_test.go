package tui

import (
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

const sampleLiveOut = `0.08 0.10 0.09 1/126 4567
MemTotal:        4010148 kB
MemFree:         1212120 kB
MemAvailable:    3300220 kB
Buffers:           12345 kB
cpu  119053 0 35055 10809042 3203 0 1283 0 0 0
cpu0 59526 0 17527 5404521 1601 0 641 0 0 0
`

func TestParseLive(t *testing.T) {
	s := parseLive(sampleLiveOut)
	if s.load != "0.08" {
		t.Errorf("load = %q, want 0.08", s.load)
	}
	if s.memTotal != 4010148*1024 || s.memAvail != 3300220*1024 {
		t.Errorf("mem = %d/%d", s.memAvail, s.memTotal)
	}
	// busy = user+nice+system+irq+softirq+steal = 119053+0+35055+0+1283+0
	if s.busy != 119053+35055+1283 {
		t.Errorf("busy = %d", s.busy)
	}
	// idle = idle+iowait = 10809042+3203
	if s.idle != 10809042+3203 {
		t.Errorf("idle = %d", s.idle)
	}
	if s.total != s.busy+s.idle {
		t.Errorf("total = %d, want busy+idle", s.total)
	}
}

func TestParseLiveEmptyAndGarbage(t *testing.T) {
	s := parseLive("")
	if s != (liveSample{}) {
		t.Fatalf("empty input = %+v, want zero", s)
	}
	s = parseLive("garbage\nmore garbage\n")
	if s != (liveSample{}) {
		t.Fatalf("garbage input = %+v, want zero", s)
	}
}

func TestSpark(t *testing.T) {
	if got := spark(nil); got != "" {
		t.Errorf("spark(nil) = %q", got)
	}
	if got := spark([]int{0, 50, 100}); got != "▁▄█" {
		t.Errorf("spark = %q, want ▁▄█", got)
	}
	// only the last 8 samples render
	if got := spark([]int{100, 100, 100, 100, 100, 100, 100, 100, 0, 0}); got != "██████▁▁" {
		t.Errorf("spark tail = %q", got)
	}
}

// liveOn returns a model on web-1's node screen with live toggled on
// and the given client as its live connection.
func liveOn(t *testing.T, client *ssh.Client) Model {
	t.Helper()
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter", "l")
	if !m.live {
		t.Fatal("l did not start live sampling")
	}
	if got := view(m); !strings.Contains(got, "connecting") {
		t.Fatalf("live strip missing connecting state:\n%s", got)
	}
	m.liveClient = client
	return m
}

func TestLiveSamplesFlow(t *testing.T) {
	m := liveOn(t, ssh.New(domain.Node{}, ""))

	// first sample: no cpu percent until a delta exists
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.10", memTotal: 100, memAvail: 50,
		busy: 100, idle: 900, total: 1000,
	}})
	if m.liveCPU != -1 {
		t.Fatalf("first sample should not guess cpu: %d", m.liveCPU)
	}

	// second sample: 100 of 1000 jiffies went to work -> 10%
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.12", memTotal: 100, memAvail: 40,
		busy: 200, idle: 1800, total: 2000,
	}})
	if m.liveCPU != 10 {
		t.Fatalf("cpu = %d, want 10", m.liveCPU)
	}
	if m.liveCur.load != "0.12" || m.liveCur.memAvail != 40 {
		t.Fatalf("sample not recorded: %+v", m.liveCur)
	}
	// cpu history only records known percents (needs two samples);
	// memory history records from the first sample on
	if len(m.liveSparkCPU) != 1 || m.liveSparkCPU[0] != 10 {
		t.Fatalf("cpu spark = %v, want [10]", m.liveSparkCPU)
	}
	if len(m.liveSparkMem) != 2 || m.liveSparkMem[1] != 60 {
		t.Fatalf("mem spark = %v, want [50 60]", m.liveSparkMem)
	}
	got := view(m)
	for _, want := range []string{"live", "10%", "▄▅", "40 B/100 B avail", "0.12"} {
		if !strings.Contains(got, want) {
			t.Errorf("live strip missing %q:\n%s", want, got)
		}
	}

	// toggle off: the strip returns to its hint, connection dropped
	m = press(t, m, "l")
	if m.live || m.liveClient != nil {
		t.Fatalf("live did not stop: live=%v client=%v", m.live, m.liveClient != nil)
	}
	got = view(m)
	if !strings.Contains(got, "[l] live") {
		t.Errorf("strip did not return to hint:\n%s", got)
	}
}

func TestLiveErrorStopsSampling(t *testing.T) {
	m := liveOn(t, ssh.New(domain.Node{}, ""))
	m = step(t, m, liveErrMsg{seq: m.liveSeq, err: errString("ssh: unreachable: refused")})
	if m.live || m.liveClient != nil {
		t.Fatal("live did not stop on error")
	}
	if m.liveErr == "" {
		t.Fatal("stop reason not recorded")
	}
	got := view(m)
	if !strings.Contains(got, "live stopped") || !strings.Contains(got, "unreachable: refused") {
		t.Errorf("strip missing the honest stop reason:\n%s", got)
	}
}

func TestLiveIgnoresStaleSessionMessages(t *testing.T) {
	m := liveOn(t, ssh.New(domain.Node{}, ""))
	stale := m.liveSeq
	m = step(t, m, liveErrMsg{seq: stale + 5, err: errString("old session died")})
	if !m.live || m.liveErr != "" {
		t.Fatal("stale error killed the current session")
	}
	// a stale ready message closes its client instead of replacing
	// the active one
	old := m.liveClient
	fresh := ssh.New(domain.Node{}, "")
	m2 := step(t, m, liveReadyMsg{seq: stale + 5, client: fresh})
	if m2.liveClient != old {
		t.Fatal("stale ready replaced the live connection")
	}
}

func TestLiveStopsWhenLeavingNode(t *testing.T) {
	m := liveOn(t, ssh.New(domain.Node{}, ""))
	m = press(t, m, "esc")
	if m.live || m.liveClient != nil {
		t.Fatal("esc did not stop live sampling")
	}
	if m.cur().kind != scFleet {
		t.Fatalf("esc did not return to the fleet: %v", m.cur().kind)
	}
}

func TestLiveStopsWhenOpeningAnotherNode(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := press(t, New(s), "n", "enter", "l")
	if !m.live {
		t.Fatal("l did not start live sampling")
	}
	m.liveClient = ssh.New(domain.Node{}, "")
	// back to the fleet, then into the other node
	m = press(t, m, "esc", "down", "enter")
	if m.live || m.liveClient != nil {
		t.Fatal("opening another node did not stop the previous live session")
	}
	if m.cur().node != "web-2" {
		t.Fatalf("did not open web-2: %q", m.cur().node)
	}
}
