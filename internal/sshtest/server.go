// Package sshtest provides an in-process SSH server for hermetic tests:
// a minimal x/crypto-based server that accepts any public key and answers
// exec requests through a test-provided handler, plus host and client key
// helpers. Nothing outside tests imports this package.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/elvonpiko/mymo/internal/domain"
)

// Handler answers one serialized exec command with its combined output
// and exit code.
type Handler func(cmd string) (string, int)

// NewHostKey generates a random host key signer.
func NewHostKey(t *testing.T) ssh.Signer {
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

// NewKey generates an ed25519 keypair, writes the private key to a temp
// file, and returns the path plus the raw private key.
func NewKey(t *testing.T) (string, ed25519.PrivateKey) {
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

// Server is a listening in-process SSH server. Close happens through
// t.Cleanup.
type Server struct {
	listener net.Listener
	config   *ssh.ServerConfig
	handler  Handler
}

// NewServer starts a server that accepts any public key and routes each
// exec request to handler.
func NewServer(t *testing.T, handler Handler) *Server {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(NewHostKey(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{listener: ln, config: cfg, handler: handler}
	t.Cleanup(func() { ln.Close() })
	go srv.serve()
	return srv
}

// Addr returns the listening address in host:port form.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Node returns a domain.Node pointing at the server with key auth.
func (s *Server) Node(t *testing.T, keyPath string) domain.Node {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.Addr())
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

// NewPasswordServer starts a faithful fresh box: it authenticates
// the given password, and accepts a public key only after the
// handler has reported its authorized_keys line installed — the
// exact trust first contact must earn, in order.
func NewPasswordServer(t *testing.T, handler Handler, password string) *Server {
	t.Helper()
	var (
		mu        sync.Mutex
		installed = map[string]bool{}
	)
	install := func(h Handler) Handler {
		return func(cmd string) (string, int) {
			out, code := h(cmd)
			for _, f := range pubLineRe.FindAllString(cmd, -1) {
				mu.Lock()
				installed[f] = true
				mu.Unlock()
			}
			return out, code
		}
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, attempt []byte) (*ssh.Permissions, error) {
			if string(attempt) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, errPasswordRejected
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			mu.Lock()
			defer mu.Unlock()
			line := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
			if installed[line] {
				return &ssh.Permissions{}, nil
			}
			return nil, errKeyNotInstalled
		},
	}
	cfg.AddHostKey(NewHostKey(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{listener: ln, config: cfg, handler: install(handler)}
	t.Cleanup(func() { ln.Close() })
	go srv.serve()
	return srv
}

var (
	errPasswordRejected = errors.New("sshtest: wrong password")
	errKeyNotInstalled  = errors.New("sshtest: key not installed")

	pubLineRe = regexp.MustCompile(`ssh-[a-z0-9@.-]+ [A-Za-z0-9+/=]{20,}`)
)

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
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

func (s *Server) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
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
