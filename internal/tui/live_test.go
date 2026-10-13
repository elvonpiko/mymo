package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
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
	// the springs carry the readings to the card: ticks drive them
	// until they settle
	settled := false
	for i := 0; i < 40 && !settled; i++ {
		mm, _ := m.Update(animTickMsg{seq: m.liveSeq})
		m = mm.(Model)
		settled = m.liveAnim.cpu.settled() && m.liveAnim.mem.settled()
	}
	if !settled {
		t.Fatal("the cpu spring never settled")
	}
	got := view(m)
	for _, want := range []string{"LIVE", "10%", "▁", "▰▱", "40 B/100 B avail", "0.12", "LOAD"} {
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
	m = press(t, m, "I")
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
	m = press(t, m, "I")

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
	m = press(t, m, "I")
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

// The network counters come from /proc/net/dev: every interface
// summed, the loopback excluded — local chatter is not the box
// talking to the world.
func TestParseLiveCountsNetworkInterfaces(t *testing.T) {
	out := "0.10 0.10 0.10 1/100 1000\n" +
		"MemTotal:       100 kB\n" +
		"MemAvailable:    50 kB\n" +
		"cpu  10 0 0 90 0 0 0 0 0 0\n" +
		"Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets\n" +
		"  lo: 999999 100 0 0 0 0 0 0 999999 100\n" +
		"  eth0: 1000 10 0 0 0 0 0 0 2000 20\n" +
		"  eth1: 300 5 0 0 0 0 0 0 50 2\n"
	s := parseLive(out)
	if s.rxBytes != 1300 || s.txBytes != 2050 {
		t.Errorf("rx/tx = %d/%d, want 1300/2050", s.rxBytes, s.txBytes)
	}
	if s.load != "0.10" || s.memTotal != 100*1024 || s.memAvail != 50*1024 {
		t.Errorf("the rest of the sample did not parse: %+v", s)
	}
}

// docker ps answers names and statuses; only "Up" containers are
// running, and the parse never invents one.
func TestParseDockerNamesTheRunning(t *testing.T) {
	out := "mymo-api-r2\tUp 3 hours\n" +
		"mymo-web-r1\tUp 2 minutes\n" +
		"old-thing\tExited (0) 5 minutes ago\n" +
		"\n"
	names := parseDocker(out)
	if len(names) != 2 || names[0] != "mymo-api-r2" || names[1] != "mymo-web-r1" {
		t.Errorf("running containers = %v", names)
	}
}

// The springs are the animation: a reading aims, ticks carry the
// number to it, and the ticker rests once it lands — a calm card
// costs nothing.
func TestSpringsCarryReadingsAndRest(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.10", memTotal: 100, memAvail: 50,
		busy: 100, idle: 900, total: 1000,
	}})
	if !m.liveAnim.ticking {
		t.Fatal("a first reading should wake the ticker")
	}
	// settle the memory spring alone
	rested := false
	for i := 0; i < 60 && !rested; i++ {
		mm, cmd := m.Update(animTickMsg{seq: m.liveSeq})
		m = mm.(Model)
		if m.liveAnim.mem.settled() && !m.liveAnim.cpu.has {
			if cmd != nil {
				t.Fatal("the ticker re-armed with nothing moving")
			}
			rested = true
			if m.liveAnim.ticking {
				t.Fatal("the ticker never rested")
			}
		}
	}
	if !rested {
		t.Fatal("the memory spring never settled")
	}
	if int(m.liveAnim.mem.pos+0.5) != 50 {
		t.Errorf("memory reading = %v, want 50", m.liveAnim.mem.pos)
	}
}

// The LIVE card carries the gadgets: network rates, the memory bar,
// and docker's running containers — each honest about what it does
// not know.
func TestLiveCardCarriesTheGadgets(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))

	now := time.Now()
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.10", memTotal: 100, memAvail: 50,
		busy: 100, idle: 900, total: 1000,
		rxBytes: 1000, txBytes: 500, at: now,
	}})
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.12", memTotal: 100, memAvail: 40,
		busy: 200, idle: 1800, total: 2000,
		rxBytes: 4000, txBytes: 2500, at: now.Add(2 * time.Second),
	}, dockOK: true, dockers: []string{"mymo-api-r2", "mymo-web-r1", "worker", "extra-1", "extra-2"}})
	for i := 0; i < 40; i++ {
		mm, _ := m.Update(animTickMsg{seq: m.liveSeq})
		m = mm.(Model)
	}
	got := ansiStrip(view(m))
	for _, want := range []string{
		"NET", "in 1.5 KiB/s", "out 1000 B/s", // 3000 and 2000 bytes over 2s
		"MEM", "▰", "LOAD", "0.12",
		"DOCK", "5 running: mymo-api-r2 mymo-web-r1 worker", "+2 more",
		"UP",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("live card missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "extra-1") {
		t.Errorf("the fold leaked a name:\n%s", got)
	}
}

// The mymo row sits under the LIVE card on a managed box: the
// applications, their releases, and the last deploy — one honest
// line. An unmanaged box keeps its air.
func TestManagedLineNamesWhatMymoManages(t *testing.T) {
	s := readyStore(t)
	n := seedNode(t, s, "web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	app := sampleApp("api", "web-1")
	app.Releases = []domain.Release{
		{ID: 1, State: domain.ReleaseRetired, DeployedAt: time.Now().Add(-3 * time.Hour)},
		{ID: 2, State: domain.ReleaseActive, DeployedAt: time.Now().Add(-25 * time.Hour)},
	}
	seedApp(t, s, app)
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	got := ansiStrip(view(m))
	for _, want := range []string{"mymo · 1 application", "2 releases", "last deploy 3 hours ago"} {
		if !strings.Contains(got, want) {
			t.Errorf("the mymo row is missing %q:\n%s", want, got)
		}
	}

	// an unmanaged box owes no row
	s2 := readyStore(t)
	seedNode(t, s2, "web-2")
	m2 := press(t, New(s2), "n", "enter")
	m2 = observe(t, m2, "web-2", richSnapshot("web-2"))
	if strings.Contains(ansiStrip(view(m2)), "mymo · ") {
		t.Errorf("an unmanaged box has no mymo row:\n%s", ansiStrip(view(m2)))
	}
}
