package facts_test

import (
	"context"
	"testing"

	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/sshtest"
)

func TestProbeOverRealTransport(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		switch cmd {
		case "uname -snrm":
			return "Linux probe-node 6.1.0-13-amd64 x86_64\n", 0
		case "cat /etc/os-release":
			return "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n", 0
		case "nproc":
			return "8\n", 0
		case "cat /proc/uptime":
			return "3600.00 0.00\n", 0
		case "cat /proc/meminfo":
			return "MemTotal:       16326432 kB\nMemAvailable:    8123456 kB\n", 0
		case "df -Pk /":
			return "Filesystem     1024-blocks      Used Available Capacity Mounted on\n" +
				"/dev/vda1        25600000  12000000  13000000      48% /\n", 0
		case "docker --version":
			return "Docker version 27.3.1, build abc\n", 0
		case "caddy version":
			return "", 127 // not installed
		case "systemctl --version":
			return "systemd 252 (252.5-2)\n", 0
		case "whoami":
			return "root\n", 0
		}
		return "", 1
	})
	node := srv.Node(t, keyPath)
	client := ssh.New(node, "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	f, err := facts.Probe(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if f.Hostname != "probe-node" || f.OS != "Debian GNU/Linux 12 (bookworm)" || f.CPUs != 8 {
		t.Fatalf("probe facts: %+v", f)
	}
	if f.Docker == "" || f.Systemd != true || f.Caddy != "" {
		t.Fatalf("stack facts: %+v", f)
	}
}
