package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/sshtest"
	"github.com/elvonpiko/mymo/internal/state"
)

// probeHandler answers the ten discovery commands with a canned
// snapshot.
func probeHandler(cmd string) (string, int) {
	switch cmd {
	case "uname -snrm":
		return "Linux probe-node 6.8.0-31-generic x86_64\n", 0
	case "cat /etc/os-release":
		return "PRETTY_NAME=\"Ubuntu 24.04.5 LTS\"\n", 0
	case "nproc":
		return "8\n", 0
	case "cat /proc/uptime":
		return "8123456.78 0.00\n", 0
	case "cat /proc/meminfo":
		return "MemTotal:       4194304 kB\nMemAvailable:    2097152 kB\n", 0
	case "df -Pk /":
		return "Filesystem     1024-blocks      Used Available Capacity Mounted on\n" +
			"/dev/vda1        25600000  12000000  13000000      48% /\n", 0
	case "docker --version":
		return "Docker version 27.3.1, build 29.1.3-0ubuntu3~24.04.2\n", 0
	case "caddy version":
		return "", 127
	case "systemctl --version":
		return "systemd 255 (255.2-1)\n", 0
	case "whoami":
		return "root\n", 0
	}
	return "", 1
}

func TestNodeCheckProbesAndPersists(t *testing.T) {
	s := newSession(t)
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, probeHandler)
	_, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	code, _, errStr := s.run(t, "node", "add",
		"-name", "probe-1", "-host", "127.0.0.1", "-port", portStr,
		"-user", "root", "-auth", "key", "-key", keyPath)
	if code != exitOK {
		t.Fatalf("add failed: %s", errStr)
	}

	code, out, errStr := s.run(t, "node", "check", "probe-1")
	if code != exitOK {
		t.Fatalf("check exit = %d, stderr: %s", code, errStr)
	}
	for _, want := range []string{
		"probe-node", "Ubuntu 24.04.5 LTS", "6.8.0-31-generic",
		"cpus:", "8", "uptime:", "94 days",
		"memory:", "4.0 GiB total", "disk (/):",
		"docker:", "27.3.1", "caddy:", "not installed",
		"systemd:", "yes", "user:", "root", "checked:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "probing") {
		t.Errorf("status line leaked into stdout:\n%s", out)
	}

	// the snapshot must be persisted with the node
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.GetNode("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Facts.Hostname != "probe-node" || n.Facts.CPUs != 8 || n.Facts.Docker == "" {
		t.Fatalf("persisted facts = %+v", n.Facts)
	}
	if n.Facts.CollectedAt.IsZero() {
		t.Fatal("facts not timestamped")
	}

	// first contact must have recorded the host key (TOFU)
	if _, err := os.Stat(filepath.Join(store.Dir(), "known_hosts.json")); err != nil {
		t.Errorf("known_hosts.json not written: %v", err)
	}
}

func TestNodeCheckSecondContactMatchesHostKey(t *testing.T) {
	s := newSession(t)
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, probeHandler)
	_, portStr, _ := net.SplitHostPort(srv.Addr())
	if code, _, errStr := s.run(t, "node", "add",
		"-name", "probe-1", "-host", "127.0.0.1", "-port", portStr,
		"-user", "root", "-auth", "key", "-key", keyPath); code != exitOK {
		t.Fatalf("add failed: %s", errStr)
	}
	for i := 0; i < 2; i++ {
		if code, _, errStr := s.run(t, "node", "check", "probe-1"); code != exitOK {
			t.Fatalf("check %d failed: %s", i+1, errStr)
		}
	}
}

func TestNodeCheckNotFound(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "check", "ghost")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "not found") {
		t.Errorf("stderr = %q, want not-found error", errStr)
	}
}

func TestNodeCheckRequiresName(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "check")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, "usage: mymo node check") {
		t.Errorf("stderr = %q, want usage", errStr)
	}
}

func TestNodeCheckUnreachableNode(t *testing.T) {
	s := newSession(t)
	keyPath, _ := sshtest.NewKey(t)
	if code, _, errStr := s.run(t, "node", "add",
		"-name", "dead-1", "-host", "127.0.0.1", "-port", "1",
		"-user", "root", "-auth", "key", "-key", keyPath); code != exitOK {
		t.Fatalf("add failed: %s", errStr)
	}
	code, _, errStr := s.run(t, "node", "check", "dead-1")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "unreachable") {
		t.Errorf("stderr = %q, want unreachable error", errStr)
	}
}

func TestNodeSSHExecsSystemSSH(t *testing.T) {
	s := newSession(t)
	keyPath, _ := sshtest.NewKey(t)
	if code, _, errStr := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-port", "2222",
		"-user", "deploy", "-auth", "key", "-key", keyPath); code != exitOK {
		t.Fatalf("add failed: %s", errStr)
	}

	// a fake ssh on PATH records its arguments and exits 7
	argsFile := filepath.Join(t.TempDir(), "ssh-args.txt")
	t.Setenv("FAKE_SSH_ARGS", argsFile)
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FAKE_SSH_ARGS\"\nexit 7\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, _, _ := s.run(t, "node", "ssh", "web-1")
	if code != 7 {
		t.Fatalf("exit code = %d, want 7 (the child's exit code)", code)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	has := func(want string) bool {
		for _, l := range got {
			if l == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"-p", "2222",
		"-i", keyPath,
		"IdentitiesOnly=yes",
		"UserKnownHostsFile=" + filepath.Join(home, ".mymo", "known_hosts"),
		"StrictHostKeyChecking=accept-new",
		"deploy@203.0.113.10",
	} {
		if !has(want) {
			t.Errorf("ssh args missing %q:\n%s", want, raw)
		}
	}
}

func TestNodeSSHUsage(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "ssh")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, "usage: mymo node ssh") {
		t.Errorf("stderr = %q, want usage", errStr)
	}
}

func TestNodeAddRejectsKeyThatDoesNotParse(t *testing.T) {
	s := newSession(t)
	// a public key file: readable, but never a private key
	pub := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(pub, []byte("ssh-ed25519 AAAAC3Nza me@here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errStr := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root",
		"-auth", "key", "-key", pub)
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "key file") {
		t.Errorf("stderr = %q, want key-file error", errStr)
	}
	// the node must not have been stored
	code, out, _ := s.run(t, "node", "list")
	if code != exitOK || !strings.Contains(out, "No nodes yet") {
		t.Errorf("rejected node leaked into state: %q", out)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{4194304, "4.0 MiB"},
		{4106280960, "3.8 GiB"},
		{42024214528, "39 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "less than a minute"},
		{30 * time.Second, "less than a minute"},
		{5 * time.Minute, "5 minutes"},
		{90 * time.Minute, "1 hour 30 minutes"},
		{42 * time.Hour, "1 day 18 hours"},
		{9*24*time.Hour + 17*time.Hour, "9 days 17 hours"},
	}
	for _, tc := range cases {
		if got := formatUptime(tc.in); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDockerVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Docker version 27.3.1, build 29.1.3-0ubuntu3~24.04.2", "27.3.1"},
		{"Docker version 24.0.7", "24.0.7"},
		{"podman 4.3.1", "podman 4.3.1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := dockerVersion(tc.in); got != tc.want {
			t.Errorf("dockerVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
