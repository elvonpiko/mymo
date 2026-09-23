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
)

// newTestHostKey generates the server's host key.
func newTestHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// writeTestKey materializes a private key file and returns its path and
// the raw private key (for the agent test).
func writeTestKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "mymo-test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, priv
}

// testServer is a minimal in-process SSH server built directly on
// x/crypto/ssh: it accepts any public key and answers exec requests
// through a test-provided handler. No third-party server dependency.
type testServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	handler  func(cmd string) (string, int)
}

func newTestServer(t *testing.T, handler func(cmd string) (string, int)) *testServer {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(newTestHostKey(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{listener: ln, config: cfg, handler: handler}
	t.Cleanup(func() { ln.Close() })
	go srv.serve()
	return srv
}

func (s *testServer) addr() string { return s.listener.Addr().String() }

// node returns a domain.Node pointing at the test server with key auth.
func (s *testServer) node(t *testing.T, keyPath string) domain.Node {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Node{
		Name:    "test-node",
		Host:    host,
		Port:    port,
		User:    "root",
		Auth:    domain.AuthKey,
		KeyPath: keyPath,
	}
}

func (s *testServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *testServer) handleConn(conn net.Conn) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
}

func (s *testServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
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
		out, code := s.handler(payload.Command)
		if code < 0 {
			code = 127
		}
		_, _ = ch.Write([]byte(out))
		_, _ = ch.SendRequest("exit-status", false,
			ssh.Marshal(struct{ Status uint32 }{Status: uint32(code)}))
		_ = ch.Close()
		return
	}
}

func TestDialRunEcho(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	srv := newTestServer(t, func(cmd string) (string, int) {
		if cmd == "uptime" {
			return "up 42 days\n", 0
		}
		return "", 0
	})
	client := New(srv.node(t, keyPath), "")
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
	keyPath, _ := writeTestKey(t)
	srv := newTestServer(t, func(cmd string) (string, int) {
		return "boom\n", 3
	})
	client := New(srv.node(t, keyPath), "")
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
	keyPath, _ := writeTestKey(t)
	var mu sync.Mutex
	var received string
	srv := newTestServer(t, func(cmd string) (string, int) {
		mu.Lock()
		received = cmd
		mu.Unlock()
		return "", 0
	})
	client := New(srv.node(t, keyPath), "")
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
	keyPath, _ := writeTestKey(t)
	srv := newTestServer(t, func(cmd string) (string, int) {
		time.Sleep(2 * time.Second)
		return "", 0
	})
	client := New(srv.node(t, keyPath), "")
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
	keyPath, _ := writeTestKey(t)
	client := New(domain.Node{Name: "x", Host: "h", Port: 22, User: "u", Auth: domain.AuthKey, KeyPath: keyPath}, "")
	if _, _, err := client.Run(context.Background(), "uptime"); err == nil {
		t.Fatal("run without dial must fail")
	}
}

func newRejectingServer(t *testing.T) (net.Listener, *ssh.ServerConfig) {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("rejected")
		},
	}
	cfg.AddHostKey(newTestHostKey(t))
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
	return ln, cfg
}

func TestDialAuthRejected(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	ln, _ := newRejectingServer(t)
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
	keyPath, _ := writeTestKey(t)
	srv := newTestServer(t, func(cmd string) (string, int) { return "", 0 })
	node := srv.node(t, keyPath)
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
	if !strings.Contains(string(data), srv.addr()) {
		t.Fatalf("known hosts missing %s:\n%s", srv.addr(), data)
	}

	// second contact must match and succeed
	c2 := New(node, knownHosts)
	if err := c2.Dial(context.Background()); err != nil {
		t.Fatalf("second dial: %v", err)
	}
	c2.Close()

	// a different key for the same host is refused
	doc := map[string]string{srv.addr(): "not-the-recorded-key"}
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
	_, priv := writeTestKey(t)

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

	keyPath, _ := writeTestKey(t)
	srv := newTestServer(t, func(cmd string) (string, int) {
		return "agent-ok\n", 0
	})
	node := srv.node(t, keyPath)
	node.Auth = domain.AuthAgent
	client := New(node, "")
	if err := client.Dial(context.Background()); err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer client.Close()
	out, code, err := client.Run(context.Background(), "whoami")
	t.Logf("DEBUG run: out=%q code=%d err=%v SSH_AUTH_SOCK=%q", out, code, err, os.Getenv("SSH_AUTH_SOCK"))
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
