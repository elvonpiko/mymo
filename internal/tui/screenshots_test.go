package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/state"
)

// TestReadmeScreenshots renders the two screens the readme shows —
// the fleet and a node's page — and pins their content: the
// screenshots stay honest because the test fails when the world
// they show changes shape. With MYMO_SHOT_DIR set, the raw ANSI
// lands there for painting; without it, this is a plain content
// test.
func TestReadmeScreenshots(t *testing.T) {
	s := readyStore(t)
	shotWorld(t, s)

	m := press(t, New(s), "n")
	if got := view(m); !strings.Contains(got, "ubuntu-…cago-01") || !strings.Contains(got, "[app-host]") {
		t.Fatalf("the fleet lost its world:\n%s", got)
	}
	shot(t, m, "fleet")

	m = press(t, m, "enter")
	if !m.loading.active {
		t.Fatal("entering the node should observe it first")
	}
	m = observe(t, m, "ubuntu-hrly-chicago-01", chicagoSnapshot(t))

	// the live card carries a real sample pair — one second apart,
	// so the net rates and the cpu share are honest numbers — and
	// the springs rest before the picture is taken
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.18", load5: "0.22", load15: "0.24",
		memAvail: 512 << 20, memTotal: 1 << 30,
		busy: 64000, idle: 193600, total: 257600,
		rxBytes: 5 << 20, txBytes: 1 << 20,
		at: time.Now().Add(-time.Second),
	}, dockOK: true, dockers: []string{"mymo-simple-go-web-r2", "caddy"}})
	m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
		load: "0.18", load5: "0.22", load15: "0.24",
		memAvail: 512 << 20, memTotal: 1 << 30,
		busy: 65120, idle: 194600, total: 259720,
		rxBytes: 5<<20 + 486000, txBytes: 1<<20 + 120000,
		at: time.Now(),
	}, dockOK: true, dockers: []string{"mymo-simple-go-web-r2", "caddy"}})
	for i := 0; i < 40; i++ {
		next, _ := m.Update(animTickMsg{seq: m.liveSeq})
		m = next.(Model)
	}

	shot(t, m, "node")
	got := ansiStrip(view(m))
	for _, want := range []string{
		"Ubuntu 26.04 LTS", "app-host",
		"mymo · 1 application · 2 releases", "last deploy 3 days ago",
		"2 running: mymo-simple-go-web-r2 caddy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the node page lost %q:\n%s", want, got)
		}
	}
}

// shot writes the raw ANSI when MYMO_SHOT_DIR names a directory;
// painting screenshots is a release task, not a test one.
func shot(t *testing.T, m Model, name string) {
	t.Helper()
	dir := os.Getenv("MYMO_SHOT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(view(m)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// shotWorld seeds the fleet the readme shows, in the shape a real
// one grows: a verified app host serving one application, a
// preparation interrupted mid-apply, and two plain watched boxes.
// Addresses stay in the documentation range — the readme's world
// is real in every way but its addresses.
func shotWorld(t *testing.T, s *state.Store) {
	t.Helper()
	now := time.Now()
	nodes := []domain.Node{
		{
			Name: "ubuntu-hrly-chicago-01", Host: "203.0.113.10", Port: 22,
			User: "mymo", Auth: domain.AuthKey, KeyPath: "/dev/null", Mode: domain.ModeAppHost,
			AddedAt: now.AddDate(0, -2, 0),
			Facts:   chicagoFacts(),
			LastCheck: domain.CheckState{
				At: now.Add(-5 * time.Minute),
			},
			Bootstrap: domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.1", At: now.AddDate(0, -2, 0)},
		},
		{
			Name: "ubuntu-hrly-chsrv-02", Host: "203.0.113.20", Port: 2238,
			User: "root", Auth: domain.AuthKey, KeyPath: "/dev/null", Mode: domain.ModeAppHost,
			AddedAt: now.AddDate(0, -1, 0),
			Facts: facts.Node{
				Hostname: "ubuntu-hrly-chsrv-02", OS: "Ubuntu 24.04 LTS", Kernel: "6.8.0-58-generic",
				Arch: "x86_64", CPUs: 2, MemTotal: 2 << 30, Docker: "27.5.1, build abc",
			},
			LastCheck: domain.CheckState{At: now.Add(-26 * time.Hour)},
			Bootstrap: domain.Bootstrap{State: domain.BootstrapApplying, At: now.AddDate(0, -1, 0)},
		},
		{
			Name: "abed-prod-01", Host: "203.0.113.30", Port: 22,
			User: "root", Auth: domain.AuthAgent, Mode: domain.ModeObserve, AddedAt: now.AddDate(0, 0, -20),
			Facts: facts.Node{
				Hostname: "abed-prod-01", OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "6.1.0-28-amd64",
				Arch: "x86_64", CPUs: 4, MemTotal: 8 << 30, Caddy: "v2.9.8",
			},
			LastCheck: domain.CheckState{At: now.Add(-40 * time.Minute)},
		},
		{
			Name: "deb-hrly-srv-01", Host: "203.0.113.40", Port: 22,
			User: "root", Auth: domain.AuthKey, KeyPath: "/dev/null", Mode: domain.ModeAppHost,
			AddedAt: now.AddDate(0, 0, -9),
			Facts: facts.Node{
				Hostname: "deb-hrly-srv-01", OS: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.0-2-amd64",
				Arch: "x86_64", CPUs: 2, MemTotal: 2 << 30, Docker: "28.4.0, build xyz",
				Caddy: "v2.10.2", Systemd: true, User: "mymo",
			},
			LastCheck: domain.CheckState{At: now.Add(-2 * time.Hour)},
			Bootstrap: domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.1", At: now.AddDate(0, 0, -9)},
		},
	}
	for _, n := range nodes {
		if err := s.AddNode(n); err != nil {
			t.Fatalf("AddNode(%s): %v", n.Name, err)
		}
	}
	app := domain.App{
		Name: "simple-go-web", Node: "ubuntu-hrly-chicago-01", Type: domain.SourceImage,
		Image: "ghcr.io/example/simple-go-web:1.4.2", Port: 8080,
		Domain: "web.example.com", Health: "/healthz", NetworkIP: "172.20.0.2",
		AddedAt: now.AddDate(0, -1, 0),
		Releases: []domain.Release{
			{ID: 1, Digest: "sha256:b3a1f0c2", Source: "image ghcr.io/example/simple-go-web:1.4.1",
				Container: "mymo-simple-go-web-r1", Health: "/healthz answers on the network",
				State: domain.ReleaseRetired, DeployedAt: now.AddDate(0, -1, 0)},
			{ID: 2, Digest: "sha256:e7d94a11", Source: "image ghcr.io/example/simple-go-web:1.4.2",
				Container: "mymo-simple-go-web-r2", Health: "/healthz answers on the network",
				State: domain.ReleaseActive, DeployedAt: now.AddDate(0, 0, -3)},
		},
	}
	if err := s.AddApp(app); err != nil {
		t.Fatalf("AddApp(): %v", err)
	}
}

// chicagoFacts is the verified app host's recorded snapshot.
func chicagoFacts() facts.Node {
	return facts.Node{
		Hostname: "ubuntu-hrly-chicago-01", OS: "Ubuntu 26.04 LTS", Kernel: "6.14.0-13-generic",
		Arch: "x86_64", CPUs: 2, Uptime: 9 * 24 * time.Hour,
		MemTotal: 1 << 30, MemAvail: 512 << 20,
		DiskTotal: 24 << 30, DiskFree: 19 << 30,
		Docker: "28.4.0, build xyz", Caddy: "v2.10.2", Systemd: true, User: "mymo",
	}
}

// chicagoSnapshot is what the live probe brings back while the
// screenshot is taken.
func chicagoSnapshot(t *testing.T) facts.Node {
	t.Helper()
	f := chicagoFacts()
	f.CollectedAt = time.Now()
	return f
}

// TestReadmeDemoRecording pins the motion the readme shows: the
// fleet, the cursor walking it, a node opened live with its gauges
// carrying numbers. With MYMO_FRAME_DIR set, every beat of the
// story lands there as a numbered frame for the demo GIF — the
// recording is the same model the screenshots are, driven the
// same way, so the GIF can never drift from the truth.
func TestReadmeDemoRecording(t *testing.T) {
	s := readyStore(t)
	shotWorld(t, s)
	frames := newFrameDumper(t)
	m := New(s)

	// home lands typing: a frame every six letters of the real
	// 28ms cadence, so the GIF plays the typing at true speed
	for m.descAnim {
		next, _ := m.Update(descTickMsg{})
		m = next.(Model)
		if m.descShown%6 == 0 {
			frames.dump(t, m, "home-type")
		}
	}
	frames.dump(t, m, "home-type")

	m = press(t, m, "n")
	frames.dump(t, m, "fleet")
	for _, k := range []string{"down", "down", "up", "up"} {
		m = press(t, m, k)
		frames.dump(t, m, "walk")
	}
	if got := ansiStrip(view(m)); !strings.Contains(got, "> ● ubuntu-…cago-01") {
		t.Fatalf("the cursor did not come home to the app host:\n%s", got)
	}

	m = press(t, m, "enter")
	// the ceremony, spinning as it really spins
	for range 5 {
		next, _ := m.Update(m.spinner.Tick())
		m = next.(Model)
		frames.dump(t, m, "load")
	}
	m = observe(t, m, "ubuntu-hrly-chicago-01", chicagoSnapshot(t))
	frames.dump(t, m, "land")

	// the gauges tell a small honest story under load — a frame
	// every two ticks, so the springs play their easing in the
	// open instead of jump-cutting between settled states
	base := time.Now()
	for i, busy := range []uint64{64000, 96000, 51000, 118000, 74000} {
		m = step(t, m, liveSampleMsg{seq: m.liveSeq, s: liveSample{
			load: "0.18", load5: "0.22", load15: "0.24",
			memAvail: (512 - uint64(i)*24) << 20, memTotal: 1 << 30,
			busy: busy, idle: 257600 - busy/2, total: 257600 + busy,
			rxBytes: uint64(300000 + i*120000), txBytes: uint64(90000 + i*30000),
			at: base.Add(time.Duration(i) * time.Second),
		}, dockOK: true, dockers: []string{"mymo-simple-go-web-r2", "caddy"}})
		for j := 0; j < 10; j++ {
			next, _ := m.Update(animTickMsg{seq: m.liveSeq})
			m = next.(Model)
			if j%2 == 1 {
				frames.dump(t, m, "live")
			}
		}
		got := ansiStrip(view(m))
		if !strings.Contains(got, "2 running: mymo-simple-go-web-r2 caddy") {
			t.Errorf("beat %d lost the dock row:\n%s", i, got)
		}
	}
}

// frameDumper writes the story's frames when MYMO_FRAME_DIR names a
// directory; without it, the story runs as a plain test.
type frameDumper struct {
	dir string
	n   int
}

func newFrameDumper(t *testing.T) frameDumper {
	t.Helper()
	dir := os.Getenv("MYMO_FRAME_DIR")
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return frameDumper{dir: dir}
}

func (f *frameDumper) dump(t *testing.T, m Model, beat string) {
	t.Helper()
	if f.dir == "" {
		return
	}
	f.n++
	name := fmt.Sprintf("%02d-%s.ansi", f.n, beat)
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(view(m)), 0o644); err != nil {
		t.Fatal(err)
	}
}
