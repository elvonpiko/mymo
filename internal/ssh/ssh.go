// Package ssh is mymo's SSH transport: one authenticated connection
// per node carrying explicit-argument-vector commands with enforced
// timeouts, structured errors, and trust-on-first-use host key
// verification. It is the only package that opens connections to nodes;
// discovery and later stages build on it.
package ssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/elvonpiko/mymo/internal/domain"
)

const (
	defaultDialTimeout    = 10 * time.Second
	defaultCommandTimeout = 15 * time.Second
)

// Client runs commands over SSH against one node. All methods are safe
// for concurrent use; commands serialize on a single connection.
type Client struct {
	node       domain.Node
	knownHosts string // host:port -> base64 key, JSON; empty path = memory

	mu         sync.Mutex
	conn       *ssh.Client
	agentConn  net.Conn
	memoryKeys map[string]string
}

// New builds a client for the node. knownHostsPath names the TOFU
// host-key store; when empty, first contact is trusted for the
// process's lifetime only.
func New(node domain.Node, knownHostsPath string) *Client {
	return &Client{
		node:       node,
		knownHosts: knownHostsPath,
		memoryKeys: make(map[string]string),
	}
}

// Dial establishes and authenticates the connection. The node's host
// key is recorded on first contact and must match on every later one.
func (c *Client) Dial(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}

	addr := net.JoinHostPort(c.node.Host, strconv.Itoa(c.node.Port))
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnreachable, addr, err)
	}

	cfg := &ssh.ClientConfig{
		User:            c.node.User,
		HostKeyCallback: c.verifyHostKey,
	}
	methods, agentConn, err := c.authMethods()
	if err != nil {
		raw.Close()
		return err
	}
	cfg.Auth = methods

	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		raw.Close()
		if agentConn != nil {
			agentConn.Close()
		}
		return classify(err, addr)
	}
	// The handshake's deadline must not bound later commands.
	_ = raw.SetDeadline(time.Time{})
	c.conn = ssh.NewClient(conn, chans, reqs)
	c.agentConn = agentConn
	return nil
}

// lockedWriter is the target for a session's combined output. x/crypto
// copies stdout and stderr from two concurrent goroutines, so the shared
// writer must serialize writes; a plain bytes.Buffer would be a data race.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Run executes one command on the node with an explicit argument
// vector and returns its combined output and exit code. A nonzero exit
// is a result, not an error; errors mean the command never completed
// (transport failure, cancellation, or timeout).
func (c *Client) Run(ctx context.Context, name string, args ...string) (string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return "", -1, errors.New("ssh: not connected")
	}
	if strings.TrimSpace(name) == "" {
		return "", -1, errors.New("ssh: empty command name")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCommandTimeout)
		defer cancel()
	}

	sess, err := c.conn.NewSession()
	if err != nil {
		return "", -1, fmt.Errorf("ssh: session: %v", err)
	}
	defer sess.Close()

	// Cancellation closes the session from the outside; Run returns
	// with the session's error and ctx tells us why.
	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-ctxDone:
		}
	}()
	defer close(ctxDone)

	out := &lockedWriter{}
	sess.Stdout = out
	sess.Stderr = out
	cmd := serialize(name, args)
	err = sess.Run(cmd)
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return out.String(), exit.ExitStatus(), nil
		}
		if ctx.Err() != nil {
			return out.String(), -1, fmt.Errorf("ssh: %q: %w", cmd, ctx.Err())
		}
		return out.String(), -1, fmt.Errorf("ssh: run %q: %v", cmd, err)
	}
	return out.String(), 0, nil
}

// Close ends the connection and any agent socket the client opened.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	if c.agentConn != nil {
		c.agentConn.Close()
		c.agentConn = nil
	}
	return nil
}

// authMethods resolves the node's configured authentication. It returns
// the agent connection (when used) so Dial can keep it open for the
// lifetime of the client.
func (c *Client) authMethods() ([]ssh.AuthMethod, net.Conn, error) {
	switch c.node.Auth {
	case domain.AuthKey:
		signer, err := loadKeySigner(c.node.KeyPath)
		if err != nil {
			return nil, nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil, nil
	case domain.AuthAgent:
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, nil, fmt.Errorf("%w: SSH_AUTH_SOCK is not set", ErrNoAgent)
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrNoAgent, err)
		}
		signers, err := agent.NewClient(conn).Signers()
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("%w: %v", ErrNoAgent, err)
		}
		if len(signers) == 0 {
			conn.Close()
			return nil, nil, fmt.Errorf("%w: agent holds no keys", ErrNoAgent)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, conn, nil
	default:
		return nil, nil, fmt.Errorf("ssh: unsupported auth method %q", c.node.Auth)
	}
}

// CheckKeyFile verifies that path names a readable private key mymo
// can actually use — the validation node add performs so a bad key
// surfaces at entry, not at first connect. Passphrase-protected keys
// are refused with a pointer at agent auth, matching the transport.
func CheckKeyFile(path string) error {
	if _, err := loadKeySigner(path); err != nil {
		return err
	}
	return nil
}

// loadKeySigner reads and parses the node's private key file.
func loadKeySigner(path string) (ssh.Signer, error) {
	p, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyRejected, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyRejected, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		var pme *ssh.PassphraseMissingError
		if errors.As(err, &pme) {
			return nil, fmt.Errorf("%w: %s is passphrase-protected; add it to the ssh agent and switch the node to agent auth", ErrKeyRejected, p)
		}
		return nil, fmt.Errorf("%w: %v", ErrKeyRejected, err)
	}
	return signer, nil
}

// expandHome resolves a leading ~ to the current user's home directory.
func expandHome(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
	}
	return path, nil
}

// verifyHostKey implements trust-on-first-use: the first contact
// records the key; every later contact must match it exactly.
func (c *Client) verifyHostKey(hostname string, _ net.Addr, key ssh.PublicKey) error {
	recorded, err := c.loadHostKey(hostname)
	if err != nil {
		return err
	}
	wire := base64.StdEncoding.EncodeToString(key.Marshal())
	if recorded == "" {
		return c.storeHostKey(hostname, wire)
	}
	if recorded != wire {
		return fmt.Errorf("%w: %s changed since it was added to mymo; refusing to connect — verify the host before trusting it again", ErrHostKeyMismatch, hostname)
	}
	return nil
}

// loadHostKey returns the recorded key for a host, or "" on first
// contact.
func (c *Client) loadHostKey(hostname string) (string, error) {
	if c.knownHosts == "" {
		return c.memoryKeys[hostname], nil
	}
	doc, err := readKnownHosts(c.knownHosts)
	if err != nil {
		return "", err
	}
	return doc[hostname], nil
}

func (c *Client) storeHostKey(hostname, key string) error {
	if c.knownHosts == "" {
		c.memoryKeys[hostname] = key
		return nil
	}
	doc, err := readKnownHosts(c.knownHosts)
	if err != nil {
		return err
	}
	doc[hostname] = key
	return writeKnownHosts(c.knownHosts, doc)
}

func readKnownHosts(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	doc := map[string]string{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("ssh: parsing %s: %v", filepath.Base(path), err)
	}
	return doc, nil
}

// writeKnownHosts persists the store atomically with restrictive
// permissions, the same discipline as the state store.
func writeKnownHosts(path string, doc map[string]string) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".known_hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// classify turns x/crypto handshake errors into mymo's structured
// errors while keeping the detail in the message.
func classify(err error, addr string) error {
	if errors.Is(err, ErrHostKeyMismatch) {
		return err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("%w: %s rejected the configured credentials", ErrAuth, addr)
	case strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no route"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "EOF"):
		return fmt.Errorf("%w: %s: %v", ErrUnreachable, addr, err)
	default:
		return fmt.Errorf("ssh: %s: %v", addr, err)
	}
}
