package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/sshtest"
)

func TestDialRunEcho(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		if cmd == "uptime" {
			return "up 42 days\n", 0
		}
		return "", 0
	})
	client := New(srv.Node(t, keyPath), "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	out, code, err := client.Run(context.Background(), "uptime")
	if err != nil || code != 0 {
		t.Fatalf("run: output=%q code=%d err=%v", out, code, err)
	}
	if !strings.Contains(out, "up 42 days") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunExitCodeIsAResult(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		return "boom\n", 3
	})
	client := New(srv.Node(t, keyPath), "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	out, code, err := client.Run(context.Background(), "systemctl", "is-active", "docker")
	if err != nil {
		t.Fatalf("nonzero exit must not be an error: %v", err)
	}
	if code != 3 {
		t.Fatalf("code = %d, want 3", code)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("output = %q", out)
	}
}

func TestRunSendsQuotedArgv(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	var mu sync.Mutex
	var received string
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		mu.Lock()
		received = cmd
		mu.Unlock()
		return "", 0
	})
	client := New(srv.Node(t, keyPath), "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.Run(context.Background(), "echo", "a b", "c$HOME", "it's"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := `echo 'a b' 'c$HOME' 'it'"'"'s'`
	if received != want {
		t.Fatalf("server received %q, want %q", received, want)
	}
}

func TestRunTimesOut(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		time.Sleep(2 * time.Second)
		return "", 0
	})
	client := New(srv.Node(t, keyPath), "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, code, err := client.Run(ctx, "sleep", "2")
	if err == nil {
		t.Fatal("timed-out run returned no error")
	}
	if code != -1 {
		t.Fatalf("code = %d, want -1", code)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not enforced: took %v", elapsed)
	}
}

func TestRunRequiresConnection(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	client := New(domain.Node{Name: "x", Host: "h", Port: 22, User: "u", Auth: domain.AuthKey, KeyPath: keyPath}, "")
	if _, _, err := client.Run(context.Background(), "uptime"); err == nil {
		t.Fatal("run without dial must fail")
	}
}

// newRejectingServer starts a server whose public-key callback refuses
// every credential.
func newRejectingServer(t *testing.T) net.Listener {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("rejected")
		},
	}
	cfg.AddHostKey(sshtest.NewHostKey(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					conn.Close()
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for range chans {
				}
			}()
		}
	}()
	return ln
}

func TestDialAuthRejected(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	ln := newRejectingServer(t)
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := New(domain.Node{Name: "x", Host: host, Port: port, User: "u", Auth: domain.AuthKey, KeyPath: keyPath}, "")
	err := client.Dial(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestDialUnreachable(t *testing.T) {
	// grab a port and release it: dialing it must be refused
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ln.Close()

	client := New(domain.Node{Name: "x", Host: host, Port: port, User: "u", Auth: domain.AuthAgent}, "")
	err = client.Dial(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestHostKeyTofuFile(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) { return "", 0 })
	node := srv.Node(t, keyPath)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts.json")

	// first contact records the key
	c1 := New(node, knownHosts)
	if err := c1.Dial(context.Background()); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	c1.Close()

	data, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("known hosts not written: %v", err)
	}
	if !strings.Contains(string(data), srv.Addr()) {
		t.Fatalf("known hosts missing %s:\n%s", srv.Addr(), data)
	}

	// second contact must match and succeed
	c2 := New(node, knownHosts)
	if err := c2.Dial(context.Background()); err != nil {
		t.Fatalf("second dial: %v", err)
	}
	c2.Close()

	// a different key for the same host is refused
	doc := map[string]string{srv.Addr(): "not-the-recorded-key"}
	if err := writeKnownHosts(knownHosts, doc); err != nil {
		t.Fatal(err)
	}
	c3 := New(node, knownHosts)
	err = c3.Dial(context.Background())
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want ErrHostKeyMismatch", err)
	}
}

func TestAgentAuth(t *testing.T) {
	_, priv := sshtest.NewKey(t)

	// an in-memory agent served over a real unix socket
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: "mymo-test"}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(ring, conn) }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)

	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		return "agent-ok\n", 0
	})
	node := srv.Node(t, keyPath)
	node.Auth = domain.AuthAgent
	client := New(node, "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer client.Close()
	out, _, err := client.Run(context.Background(), "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agent-ok") {
		t.Fatalf("output = %q", out)
	}
}

func TestPassphraseKeyRejected(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "mymo-test", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_encrypted")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadKeySigner(path)
	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("err = %v, want ErrKeyRejected", err)
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Fatalf("error should point at the agent escape hatch: %v", err)
	}
}

func TestMissingKeyFileRejected(t *testing.T) {
	_, err := loadKeySigner(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("err = %v, want ErrKeyRejected", err)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/", home},
		{"~/.ssh/id_ed25519", filepath.Join(home, ".ssh/id_ed25519")},
		{"/etc/passwd", "/etc/passwd"},
	}
	for _, tc := range cases {
		got, err := expandHome(tc.in)
		if err != nil {
			t.Errorf("expandHome(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := expandHome(""); err == nil {
		t.Error("empty path should be rejected")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, no supported methods remain"), ErrAuth},
		{errors.New("ssh: handshake failed: read tcp 1.2.3.4:1->1.2.3.5:22: i/o timeout"), ErrUnreachable},
		{fmt.Errorf("ssh: handshake failed: %w", ErrHostKeyMismatch), ErrHostKeyMismatch},
	}
	for _, tc := range cases {
		if got := classify(tc.err, "h:1"); !errors.Is(got, tc.want) {
			t.Errorf("classify(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
