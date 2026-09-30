package facts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers probe commands from canned responses, recording
// what was asked for.
type fakeRunner struct {
	responses map[string]fakeResult
	asked     []string
}

type fakeResult struct {
	out  string
	code int
	err  error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.asked = append(f.asked, cmd)
	r, ok := f.responses[cmd]
	if !ok {
		return "", 1, nil
	}
	return r.out, r.code, r.err
}

func TestParsers(t *testing.T) {
	t.Run("uname", func(t *testing.T) {
		host, kernel, arch := parseUname("Linux abed-prod-01 6.1.0-13-amd64 x86_64\n")
		if host != "abed-prod-01" || kernel != "6.1.0-13-amd64" || arch != "x86_64" {
			t.Fatalf("got %q %q %q", host, kernel, arch)
		}
		if host, _, _ := parseUname("garbage"); host != "" {
			t.Fatalf("partial uname should leave facts unset, got %q", host)
		}
	})

	t.Run("os-release", func(t *testing.T) {
		out := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
		if got := parseOSRelease(out); got != "Debian GNU/Linux 12 (bookworm)" {
			t.Fatalf("got %q", got)
		}
		if got := parseOSRelease("ID=alpine\n"); got != "" {
			t.Fatalf("missing PRETTY_NAME should be empty, got %q", got)
		}
		if got := parseOSRelease("PRETTY_NAME=Ubuntu 24.04 LTS\n"); got != "Ubuntu 24.04 LTS" {
			t.Fatalf("unquoted value: %q", got)
		}
	})

	t.Run("uptime", func(t *testing.T) {
		if got := parseUptime("8123456.78 123.45\n"); got != 8123456*time.Second {
			t.Fatalf("got %v", got)
		}
		if got := parseUptime("nonsense"); got != 0 {
			t.Fatalf("garbage should be zero, got %v", got)
		}
	})

	t.Run("meminfo", func(t *testing.T) {
		out := "MemTotal:       16326432 kB\nMemFree:          1234567 kB\nMemAvailable:    8123456 kB\n"
		total, avail := parseMemInfo(out)
		if total != 16326432*1024 || avail != 8123456*1024 {
			t.Fatalf("got total=%d avail=%d", total, avail)
		}
		// pre-3.14 kernels have no MemAvailable
		total, avail = parseMemInfo("MemTotal: 100 kB\n")
		if total != 100*1024 || avail != 0 {
			t.Fatalf("missing MemAvailable should stay zero, got %d", avail)
		}
	})

	t.Run("df", func(t *testing.T) {
		out := "Filesystem     1024-blocks      Used Available Capacity Mounted on\n" +
			"/dev/vda1        25600000  12000000  13000000      48% /\n"
		total, free := parseDF(out)
		if total != 25600000*1024 || free != 13000000*1024 {
			t.Fatalf("got total=%d free=%d", total, free)
		}
		if _, free := parseDF("Filesystem 1024-blocks\n"); free != 0 {
			t.Fatal("header-only df should be zero")
		}
	})

	t.Run("firstLine", func(t *testing.T) {
		if got := firstLine("\nDocker version 27.3.1, build abc\n\n"); got != "Docker version 27.3.1, build abc" {
			t.Fatalf("got %q", got)
		}
		if got := firstLine("   "); got != "" {
			t.Fatalf("blank output: %q", got)
		}
	})
}

func TestProbeParsesFullSnapshot(t *testing.T) {
	r := &fakeRunner{responses: map[string]fakeResult{
		"uname -snrm":         {"Linux abed-prod-01 6.1.0-13-amd64 x86_64\n", 0, nil},
		"cat /etc/os-release": {"PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n", 0, nil},
		"nproc":               {"8\n", 0, nil},
		"cat /proc/uptime":    {"8123456.78 0.00\n", 0, nil},
		"cat /proc/meminfo":   {"MemTotal:       16326432 kB\nMemAvailable:    8123456 kB\n", 0, nil},
		"df -Pk /": {
			"Filesystem     1024-blocks      Used Available Capacity Mounted on\n" +
				"/dev/vda1        25600000  12000000  13000000      48% /\n", 0, nil},
		"docker --version":    {"Docker version 27.3.1, build abc\n", 0, nil},
		"caddy version":       {"v2.8.4\n", 0, nil},
		"systemctl --version": {"systemd 252 (252.5-2)\n", 0, nil},
		"whoami":              {"amir\n", 0, nil},
	}}
	f, err := Probe(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if f.Hostname != "abed-prod-01" || f.Kernel != "6.1.0-13-amd64" || f.Arch != "x86_64" {
		t.Errorf("uname facts: %+v", f)
	}
	if f.OS != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("os = %q", f.OS)
	}
	if f.CPUs != 8 {
		t.Errorf("cpus = %d", f.CPUs)
	}
	if f.Uptime != 8123456*time.Second {
		t.Errorf("uptime = %v", f.Uptime)
	}
	if f.MemTotal != 16326432*1024 || f.MemAvail != 8123456*1024 {
		t.Errorf("mem: %+v", f)
	}
	if f.DiskTotal != 25600000*1024 || f.DiskFree != 13000000*1024 {
		t.Errorf("disk: %+v", f)
	}
	if f.Docker != "Docker version 27.3.1, build abc" || f.Caddy != "v2.8.4" {
		t.Errorf("stack: %+v", f)
	}
	if !f.Systemd {
		t.Error("systemd should be true")
	}
	if f.User != "amir" {
		t.Errorf("user = %q", f.User)
	}
	if f.CollectedAt.IsZero() {
		t.Error("collected at should be set")
	}
}

func TestProbeToleratesMissingCommands(t *testing.T) {
	// a bare node: no docker, no caddy, no systemd — everything else
	// still collects.
	r := &fakeRunner{responses: map[string]fakeResult{
		"uname -snrm":         {"Linux bare 6.1.0 x86_64\n", 0, nil},
		"nproc":               {"1\n", 0, nil},
		"cat /etc/os-release": {"", 1, nil}, // some minimal images have none
		"docker --version":    {"", 127, nil},
		"caddy version":       {"", 127, nil},
		"systemctl --version": {"", 127, nil},
	}}
	f, err := Probe(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if f.Hostname != "bare" || f.CPUs != 1 {
		t.Errorf("present facts should collect: %+v", f)
	}
	if f.Docker != "" || f.Caddy != "" || f.Systemd || f.OS != "" {
		t.Errorf("absent tools must stay zero: %+v", f)
	}
}

func TestProbeAbortsOnTransportError(t *testing.T) {
	r := &fakeRunner{responses: map[string]fakeResult{
		"uname -snrm": {"", 0, errors.New("ssh: connection lost")},
	}}
	if _, err := Probe(context.Background(), r); err == nil {
		t.Fatal("transport error must abort the probe")
	}
	// the first command doubles as the reachability check: nothing else runs
	if len(r.asked) != 1 {
		t.Fatalf("asked = %v, want just the first command", r.asked)
	}
}

func TestProbeReadsTheBaselineMarker(t *testing.T) {
	r := &fakeRunner{responses: map[string]fakeResult{
		"uname -snrm": {"Linux web-1 6.1.0 x86_64\n", 0, nil},
		"sh -c test -f /var/lib/mymo/baseline.json && cat /var/lib/mymo/baseline.json": {
			"{\"baseline\": \"0.1\", \"appliedAt\": \"2026-02-15T10:00:00Z\"}\n", 0, nil},
	}}
	f, err := Probe(context.Background(), r)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if f.BaselineMarker == "" {
		t.Fatal("the probe did not read the marker")
	}
	rec, ok := ParseBaselineMarker(f.BaselineMarker)
	if !ok || rec.Baseline != "0.1" || rec.AppliedAt != "2026-02-15T10:00:00Z" {
		t.Errorf("parsed = %+v ok=%v", rec, ok)
	}

	// a hand-mangled marker is not a record mymo trusts
	if _, ok := ParseBaselineMarker("not json"); ok {
		t.Error("garbage parsed as a marker")
	}
	if _, ok := ParseBaselineMarker(`{"baseline": "0.1"}`); ok {
		t.Error("a marker without appliedAt parsed as a record")
	}
}
