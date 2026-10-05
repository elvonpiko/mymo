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
	"io"
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
	defaultDialTimeout = 10 * time.Second
)

// dialAttempts and dialAttemptPause shape the bounded retry a connect
// failure earns: a fresh box is often still booting, its sshd not yet
// listening, and one ten-second try gives up right before the box
// would have answered. The budget is three tries of ten seconds with
// a short pause — a blackhole costs half a minute, no more, and a
// cancel stops the whole ceremony mid-pause. Authentication is never
// retried: a wrong password is an answer, not a condition, and
// hammering a box earns fail2ban, not a connection.
var (
	dialAttempts     = 3
	dialAttemptPause = time.Second
)

const (
	defaultCommandTimeout = 15 * time.Second
	// defaultTransferTimeout bounds a context transfer when the
	// caller sets no deadline; big trees over slow links get slack.
	defaultTransferTimeout = 10 * time.Minute
)

// Client runs commands over SSH against one node. All methods are safe
// for concurrent use; commands serialize on a single connection.
type Client struct {
	node       domain.Node
	knownHosts string // host:port -> base64 key, JSON; empty path = memory

	// password is first-contact-only: set on a client that connects
	// once with the provider's password to install a key, then goes
	// out of scope. It is never written anywhere — no field on any
	// persisted record can hold one.
	password string

	// progress receives one line per connect retry — the ceremonies
	// surface it so a slow or booting box reads as "still trying",
	// never as a hang. Nil means silent.
	progress func(string)

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

// WithProgress attaches the dial's narrator: one line per connect
// retry, and the steps first contact reaches. Nil silences it. The
// client returns for chaining.
func (c *Client) WithProgress(fn func(string)) *Client {
	c.progress = fn
	return c
}

// NewFirstContact builds the one-shot client that authenticates with
// a password: the spec's temporary password authentication for
// bootstrap. The password lives in this client's memory for the
// single connection it exists to make and is never persisted.
func NewFirstContact(node domain.Node, knownHostsPath, password string) *Client {
	c := New(node, knownHostsPath)
	c.password = password
	return c
}

// AppendAuthorizedKey adds one public key line to the node user's
// authorized_keys, append-only and idempotent: an identical line is
// never duplicated, and the file is never rewritten — the operator's
// other keys are untouchable. This is the only mutation mymo makes
// before a confirmed plan, and it happens only through first
// contact, with the password the operator typed for exactly this.
func (c *Client) AppendAuthorizedKey(ctx context.Context, pubLine string) error {
	script := "mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && " +
		"grep -qxF " + shellQuote(pubLine) + " ~/.ssh/authorized_keys || " +
		"echo " + shellQuote(pubLine) + " >> ~/.ssh/authorized_keys; " +
		"chmod 600 ~/.ssh/authorized_keys"
	if out, code, err := c.Run(ctx, "sh", "-c", script); err != nil || code != 0 {
		return fmt.Errorf("installing the key on the node failed (exit %d): %s", code, firstLineOf(out))
	}
	return nil
}

// firstLineOf keeps failure notes to one row.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Dial establishes and authenticates the connection. The node's host
// key is recorded on first contact and must match on every later one.
func (c *Client) Dial(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	addr := net.JoinHostPort(c.node.Host, strconv.Itoa(c.node.Port))

	var last error
	for attempt := 1; attempt <= dialAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			// the caller's own budget or cancel ends the retries
			return err
		}
		if attempt > 1 {
			if c.progress != nil {
				c.progress(fmt.Sprintf("still connecting to %s — try %d of %d (%s)",
					addr, attempt, dialAttempts, retryCause(last)))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(dialAttemptPause):
			}
		}
		last = c.dialOnce(ctx, addr)
		if last == nil {
			return nil
		}
		if !errors.Is(last, ErrUnreachable) {
			// authentication refused, the host key changed, or the
			// caller's context ended: none of these is "try again"
			return last
		}
	}
	return &connectError{detail: connectDetail(last) +
		fmt.Sprintf(" — tried %d times, the box may still be booting or the address is wrong", dialAttempts)}
}

// dialOnce makes one full attempt — TCP and SSH handshake — under a
// per-try budget; a caller's earlier deadline still bounds it.
func (c *Client) dialOnce(ctx context.Context, addr string) error {
	tryCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()

	raw, err := (&net.Dialer{}).DialContext(tryCtx, "tcp", addr)
	if err != nil {
		return &connectError{detail: addr + ": " + err.Error()}
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

	if deadline, ok := tryCtx.Deadline(); ok {
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

// connectDetail keeps the transport's own words — i/o timeout,
// connection refused — in the final report, minus mymo's wrappers.
func connectDetail(err error) string {
	var un *connectError
	if errors.As(err, &un) {
		return un.detail
	}
	return err.Error()
}

// connectError is the unreachable wrapper: mymo's sentinel in the
// chain, the transport's words kept for the report.
type connectError struct {
	detail string
}

func (e *connectError) Error() string {
	return ErrUnreachable.Error() + ": " + e.detail
}

func (e *connectError) Unwrap() error { return ErrUnreachable }

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
		if c.password != "" {
			// first contact: the one connection the password exists
			// for — it is offered as an auth method, never stored
			return []ssh.AuthMethod{ssh.Password(c.password)}, nil, nil
		}
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

// retryCause names a failed try in a few honest words for the
// loading card; the full detail stays in the final error.
func retryCause(err error) string {
	detail := strings.ToLower(connectDetail(err))
	causes := []string{
		"i/o timeout", "connection refused", "no route",
		"connection reset", "eof",
	}
	for _, c := range causes {
		if strings.Contains(detail, c) {
			return c
		}
	}
	return "no answer"
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
		return &connectError{detail: addr + ": " + err.Error()}
	default:
		return fmt.Errorf("ssh: %s: %v", addr, err)
	}
}

// Copy extracts a gzipped tarball into dest on the node, creating it
// when missing: the bytes stream over the session's stdin, so a
// build context travels without a second connection or a staging
// file on either side. sudo prefixes the extraction when the
// operator is not root — the stream flows through it.
func (c *Client) Copy(ctx context.Context, dest string, tarball io.Reader, sudo bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("ssh: not connected")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTransferTimeout)
		defer cancel()
	}

	sess, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh: session: %v", err)
	}
	defer sess.Close()
	// no pty: the stream is binary and must arrive unmangled, and
	// sudo -n does not need a terminal
	sess.Stdin = tarball
	cmd := "sh -c " + shellQuote("mkdir -p "+dest+" && tar -xzf - -C "+dest)
	if sudo {
		cmd = "sudo -n " + cmd
	}

	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-ctxDone:
		}
	}()
	defer close(ctxDone)

	if err := sess.Run(cmd); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ssh: copy to %s: %w", dest, ctx.Err())
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("extracting the build context failed on the node (exit %d)", exit.ExitStatus())
		}
		return fmt.Errorf("ssh: copy to %s: %v", dest, err)
	}
	return nil
}
