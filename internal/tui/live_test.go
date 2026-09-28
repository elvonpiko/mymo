package tui

import (
	"strings"
	"testing"
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
	if s.load != "0.08" || s.load5 != "0.10" || s.load15 != "0.09" {
		t.Errorf("loads = %q %q %q, want 0.08 0.10 0.09", s.load, s.load5, s.load15)
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
	if s := parseLive(""); s != (liveSample{}) {
		t.Errorf("empty input = %+v, want zero", s)
	}
	if s := parseLive("garbage\nmore garbage\n"); s != (liveSample{}) {
		t.Errorf("garbage input = %+v, want zero", s)
	}
}

func TestSpark(t *testing.T) {
	if got := spark(nil); got != "" {
		t.Errorf("spark(nil) = %q", got)
	}
	if got := spark([]int{0, 50, 100}); got != "▁▄█" {
		t.Errorf("spark = %q, want ▁▄█", got)
	}
	// a short history renders whole
	if got := spark([]int{100, 100, 100, 100, 100, 100, 100, 100, 0, 0}); got != "████████▁▁" {
		t.Errorf("spark tail = %q", got)
	}
	// only the last liveSparkLen samples render
	all := make([]int, 20)
	for i := range all {
		all[i] = 100
	}
	if got := spark(all); got != "████████████" {
		t.Errorf("spark long = %q", got)
	}
}

func TestLiveSamplesFlow(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	if !m.live || m.liveClient == nil {
		t.Fatal("observation did not adopt the probe connection for live")
	}

	// first sample: no cpu percent until a delta exists
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.10", memTotal: 100, memAvail: 50,
		busy: 100, idle: 900, total: 1000,
	}})
	if m.liveCPU != -1 {
		t.Fatalf("first sample should not guess cpu: %d", m.liveCPU)
	}
	if got := view(m); !strings.Contains(got, "warming") {
		t.Errorf("cpu should say warming before two samples:\n%s", got)
	}

	// second sample: 100 of 1000 new jiffies went to work -> 10%
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
	for _, want := range []string{"LIVE", "10%", "▄▅", "40 B/100 B avail", "0.12", "LOAD"} {
		if !strings.Contains(got, want) {
			t.Errorf("live card missing %q:\n%s", want, got)
		}
	}

	// leaving the node drops the connection and the live line's data
	m = press(t, m, "esc")
	if m.live || m.liveClient != nil {
		t.Fatalf("esc did not stop live: live=%v client=%v", m.live, m.liveClient != nil)
	}
	if m.cur().kind != scFleet {
		t.Fatalf("esc did not return to the fleet: %v", m.cur().kind)
	}
}

func TestLiveErrorStopsSampling(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = step(t, m, liveErrMsg{seq: m.liveSeq, err: errString("ssh: unreachable: refused")})
	if m.live || m.liveClient != nil {
		t.Fatal("live did not stop on error")
	}
	if m.liveErr == "" {
		t.Fatal("stop reason not recorded")
	}
	got := view(m)
	if !strings.Contains(got, "stopped") || !strings.Contains(got, "unreachable: refused") {
		t.Errorf("live card missing the honest stop reason:\n%s", got)
	}
	// the stopped card keeps the last known memory, labeled a snapshot
	if !strings.Contains(got, "(snapshot)") {
		t.Errorf("stopped card missing the snapshot marker:\n%s", got)
	}
}

func TestLiveIgnoresStaleSessionMessages(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	// a message from a superseded session must not stop the live one
	m = step(t, m, liveErrMsg{seq: m.liveSeq + 5, err: errString("old session died")})
	if !m.live || m.liveErr != "" {
		t.Fatal("stale error killed the current session")
	}
	m = step(t, m, liveSampleMsg{seq: m.liveSeq + 5, s: liveSample{load: "9.99"}})
	if m.liveCur.load == "9.99" {
		t.Fatal("stale sample corrupted the current session")
	}
}

func TestEnteringAnotherNodeStopsLive(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	if !m.live || m.liveClient == nil {
		t.Fatal("live session not running")
	}
	// back to the fleet, then into the other node — a new observation
	m = press(t, m, "esc", "down", "enter")
	if m.live || m.liveClient != nil {
		t.Fatal("opening another node did not stop the previous live session")
	}
	if m.cur().node != "web-2" || !m.loading.active || m.loading.node != "web-2" {
		t.Fatalf("did not start observing web-2: screen=%q active=%v", m.cur().node, m.loading.active)
	}
}

func TestSupersededProbeRecordsWithoutDisturbingTheNew(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := press(t, New(s), "n", "enter")
	oldSeq := m.checkSeq
	// leave before web-1's probe answers, start observing web-2
	m = press(t, m, "esc", "down", "enter")
	if m.checkSeq == oldSeq || !m.loading.active || m.loading.node != "web-2" {
		t.Fatalf("web-2 observation not running: seq=%d active=%v", m.checkSeq, m.loading.active)
	}
	// web-1's late result lands: recorded and toasted, but web-2's
	// loading page keeps running untouched
	m = step(t, m, checkDoneMsg{
		node: "web-1", snap: richSnapshot("web-1"),
		client: fakeClient(), seq: oldSeq,
	})
	if !m.loading.active || m.loading.node != "web-2" {
		t.Fatalf("late result disturbed web-2's probe: active=%v node=%q", m.loading.active, m.loading.node)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "checked web-1") {
		t.Fatalf("no toast for the missed result: %+v", m.toast)
	}
	if m.liveClient != nil {
		t.Fatal("late result adopted its connection off-page")
	}
	n, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Facts.OS != "Ubuntu 24.04.5 LTS" {
		t.Fatalf("superseded probe result not recorded: %+v", n.Facts)
	}
}

func TestAuditBorrowsTheLiveConnectionSafely(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	if !m.live || m.liveClient == nil {
		t.Fatal("live session not running before the audit")
	}

	// the audit borrows the connection: sampling pauses, the
	// connection stays open for the audit to use
	m = press(t, m, "p")
	if m.live || !m.livePaused || m.liveClient == nil {
		t.Fatalf("audit did not pause sampling cleanly: live=%v paused=%v client=%v",
			m.live, m.livePaused, m.liveClient != nil)
	}
	if !m.auditBusy {
		t.Fatal("audit did not mark the connection busy")
	}

	// the hazard this guards: a slow tick times out mid-audit. It
	// must be ignored — never close the connection under the audit.
	m = step(t, m, liveErrMsg{seq: m.liveSeq, err: errString("ssh: i/o timeout")})
	if m.liveClient == nil {
		t.Fatal("a timed-out tick closed the connection the audit is using")
	}
	if m.live {
		t.Fatal("sampling resumed while the audit still holds the connection")
	}

	// the audit lands with the user still on its page: still paused
	m = step(t, m, pfDoneMsg{node: "web-1", snap: richSnapshot("web-1"), audit: cannedAudit()})
	if m.auditBusy {
		t.Fatal("audit busy flag never cleared")
	}
	if m.live || m.livePaused != true {
		t.Fatalf("sampling must stay paused off the observe page: live=%v paused=%v", m.live, m.livePaused)
	}

	// coming home resumes over the kept connection
	m = press(t, m, "esc")
	if !m.live || m.livePaused {
		t.Fatalf("returning to the observe page must resume sampling: live=%v paused=%v", m.live, m.livePaused)
	}
}

func TestEscDuringAuditWaitsForTheAuditToLand(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = press(t, m, "p")

	// the user walks home while the audit still runs in the
	// background: no resume until the connection is free again
	m = press(t, m, "esc")
	if m.cur().kind != scNode {
		t.Fatalf("esc did not return to the observe page: %v", m.cur().kind)
	}
	if m.live {
		t.Fatal("sampling resumed while the audit still runs in the background")
	}

	// the late audit lands: the connection is free, sampling resumes
	m = step(t, m, pfDoneMsg{node: "web-1", snap: richSnapshot("web-1"), audit: cannedAudit()})
	if !m.live {
		t.Fatal("the late audit did not hand the connection back to sampling")
	}
}

func TestPlanFlowPausesSamplingToo(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	snap := richSnapshot("web-1")
	snap.Docker = ""
	m = observe(t, m, "web-1", snap)
	m = press(t, m, "p")
	m = step(t, m, pfDoneMsg{node: "web-1", snap: snap, audit: cannedAudit()})

	m = press(t, m, "enter")
	if m.live || !m.livePaused {
		t.Fatalf("plan generation did not pause sampling: live=%v paused=%v", m.live, m.livePaused)
	}
	m = step(t, m, planMsg{node: "web-1"})
	if m.live {
		t.Fatal("sampling resumed on the plan page — the LIVE card is not even visible")
	}
	m = press(t, m, "esc") // back to the audit's verdicts
	if m.live {
		t.Fatal("sampling resumed on the preflight page")
	}
	m = press(t, m, "esc") // home to the observe page
	if !m.live {
		t.Fatal("sampling did not resume on the observe page")
	}
}
