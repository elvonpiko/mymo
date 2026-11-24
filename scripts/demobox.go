//go:build ignore

// Command demobox is a tiny SSH peer for the readme recording.
// It answers the same read-only commands mymo's observe page runs,
// and the numbers move a little on every sample, so the gauges in
// the recording are the real interface, not a painted still.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "address to listen on")
	flag.Parse()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal(err)
	}
	fmt.Println(ln.Addr().String())
	for {
		conn, err := ln.Accept()
		if err != nil {
			fatal(err)
		}
		go handle(conn, cfg)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "demobox:", err)
	os.Exit(1)
}

func handle(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go session(channel, requests)
	}
}

func session(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			_ = ch.Close()
			return
		}
		_ = req.Reply(true, nil)
		out, code := answer(payload.Command)
		_, _ = ch.Write([]byte(out))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
		_ = ch.Close()
		return
	}
}

// sample counts how many live reads have happened, so each one is a
// little busier than the last and the gauges have something to chase.
var sample atomic.Uint64

func answer(cmd string) (string, int) {
	switch {
	case strings.HasPrefix(cmd, "uname "):
		return "Linux chicago 6.8.0-45-generic x86_64\n", 0
	case strings.Contains(cmd, "/etc/os-release"):
		return "PRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n", 0
	case cmd == "nproc":
		return "2\n", 0
	case strings.Contains(cmd, "/proc/uptime"):
		return "864000.00 1728000.00\n", 0
	case strings.Contains(cmd, "/proc/meminfo") && !strings.Contains(cmd, "loadavg"):
		return "MemTotal:        2048000 kB\nMemAvailable:    1126400 kB\n", 0
	case strings.HasPrefix(cmd, "df "):
		return "Filesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/vda1         52428800  12582912  39845888      24% /\n", 0
	case strings.HasPrefix(cmd, "docker --version"):
		return "Docker version 28.4.0, build 1234567\n", 0
	case strings.HasPrefix(cmd, "caddy version"):
		return "v2.10.2\n", 0
	case strings.HasPrefix(cmd, "systemctl "):
		return "systemd 255 (255.4-1ubuntu8)\n", 0
	case cmd == "whoami":
		return "mymo\n", 0
	case strings.Contains(cmd, "baseline.json"):
		return "{\"baseline\": \"0.1\", \"appliedAt\": \"2026-08-01T09:00:00Z\"}\n", 0
	case strings.Contains(cmd, "/proc/loadavg"):
		return live(), 0
	case strings.Contains(cmd, "docker") && strings.Contains(cmd, "ps"):
		return "mymo-web-r2\tUp 3 days\ncaddy\tUp 8 weeks\n", 0
	default:
		return "", 127
	}
}

func live() string {
	n := sample.Add(1)
	user := 8000 + n*180
	idle := 40000 + n*220
	rx := 8_000_000 + n*240_000
	tx := 1_500_000 + n*60_000
	return fmt.Sprintf("0.42 0.31 0.28 1/142 9001\n"+
		"MemTotal:        2048000 kB\nMemAvailable:    %d kB\n"+
		"cpu  %d 80 900 %d 120 40 30 0 0 0\n"+
		"eth0: %d 100 0 0 0 0 0 0 %d 80 0 0 0 0 0 0\n"+
		"lo: 100 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n",
		1126400-n*4096, user, idle, rx, tx)
}
