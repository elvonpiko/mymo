package ssh

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/elvonpiko/mymo/internal/sshtest"
)

// pubLineRegexp is the one shape an authorized_keys line can take.
var pubLineRegexp = regexp.MustCompile(`ssh-[a-z0-9@.-]+ [A-Za-z0-9+/=]{20,}`)

// fakeBox simulates a node's authorized_keys semantics faithfully:
// grep -qxF answers whether the line is present, echo appends it, and
// every append is counted — so the tests can prove the install is
// append-only and never duplicated.
type fakeBox struct {
	mu       sync.Mutex
	lines    []string
	appends  int
	whoamiAs string
}

func (b *fakeBox) handler(cmd string) (string, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case strings.Contains(cmd, "whoami"):
		return b.whoamiAs, 0
	case strings.Contains(cmd, "authorized_keys"):
		// the whole install runs as one sh -c: "grep -qxF … || echo
		// … >> …" — present means grep answers and the append never
		// runs; absent means the append runs
		m := pubLineRegexp.FindString(cmd)
		if m == "" {
			return "no public key in the install", 1
		}
		for _, l := range b.lines {
			if l == m {
				return "", 0
			}
		}
		b.lines = append(b.lines, m)
		b.appends++
		return "", 0
	}
	return "", 0
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestFirstContactEndToEnd(t *testing.T) {
	const password = "provider-mailed-this"
	box := &fakeBox{whoamiAs: "root"}
	srv := sshtest.NewPasswordServer(t, box.handler, password)
	host, port := hostPort(t, srv.Addr())

	keyPath := filepath.Join(t.TempDir(), "web-1.key")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts.json")

	if err := FirstContact(context.Background(), host, port, "root", password, keyPath, knownHosts, nil); err != nil {
		t.Fatalf("FirstContact() = %v, want nil", err)
	}
	box.mu.Lock()
	if len(box.lines) != 1 || box.appends != 1 {
		t.Fatalf("installed lines = %d, appends = %d; want 1 and 1", len(box.lines), box.appends)
	}
	box.mu.Unlock()

	// the dedicated pair exists where the caller asked for it
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("dedicated key missing: %v", err)
	}
	// both connections recorded the host key in the same trust store
	if b, err := os.ReadFile(knownHosts); err != nil || len(b) == 0 {
		t.Fatalf("first contact did not record the host key: %v", err)
	}

	// a retry is idempotent: grep answers "present", the append
	// never runs, and the flow still ends proven
	if err := FirstContact(context.Background(), host, port, "root", password, keyPath, knownHosts, nil); err != nil {
		t.Fatalf("retry FirstContact() = %v, want nil", err)
	}
	box.mu.Lock()
	if box.appends != 1 {
		t.Fatalf("appends = %d after retry, want 1 — the install is not idempotent", box.appends)
	}
	box.mu.Unlock()
}

// TestFirstContactExpiredPasswordNamesTheCondition reproduces the
// real fresh-thing the user met: Ubuntu's forced reset answers the
// first command with its banner and a refusal, and mymo must name
// the condition instead of reporting a nameless exit 1.
func TestFirstContactExpiredPasswordNamesTheCondition(t *testing.T) {
	box := &expiredBox{}
	srv := sshtest.NewPasswordServer(t, box.handler, "provider-mailed-this")
	host, port := hostPort(t, srv.Addr())

	err := FirstContact(context.Background(), host, port, "ubuntu", "provider-mailed-this",
		filepath.Join(t.TempDir(), "web-1.key"), filepath.Join(t.TempDir(), "known_hosts.json"), nil)
	if !errors.Is(err, ErrPasswordExpired) {
		t.Fatalf("err = %v, want ErrPasswordExpired", err)
	}
	if !strings.Contains(err.Error(), "idempotent") {
		t.Errorf("the refusal must teach the retry move: %v", err)
	}
}

// expiredBox answers every command the way a forced-reset account
// does: the MOTD banner and a dead exit code.
type expiredBox struct{}

func (b *expiredBox) handler(cmd string) (string, int) {
	if strings.Contains(cmd, "authorized_keys") {
		return "WARNING: Your password has expired.\nYou must change your password now and login again!", 1
	}
	return "You are required to change your password immediately (administrator enforced).", 1
}

func TestFirstContactWrongPasswordIsRefused(t *testing.T) {
	box := &fakeBox{whoamiAs: "root"}
	srv := sshtest.NewPasswordServer(t, box.handler, "right")
	host, port := hostPort(t, srv.Addr())

	err := FirstContact(context.Background(), host, port, "root", "wrong",
		filepath.Join(t.TempDir(), "web-1.key"), filepath.Join(t.TempDir(), "known_hosts.json"), nil)
	if err == nil {
		t.Fatal("wrong password must fail first contact")
	}
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	if box.appends != 0 {
		t.Fatalf("appends = %d, want 0 — nothing may be installed on a failed login", box.appends)
	}
}

func TestFirstContactWrongLoginUserIsRefused(t *testing.T) {
	const password = "pw"
	// the key authenticates, but whoami answers another user — the
	// flow must refuse rather than save a node that logs in wrong
	box := &fakeBox{whoamiAs: "someone-else"}
	srv := sshtest.NewPasswordServer(t, box.handler, password)
	host, port := hostPort(t, srv.Addr())

	err := FirstContact(context.Background(), host, port, "root", password,
		filepath.Join(t.TempDir(), "web-1.key"), filepath.Join(t.TempDir(), "known_hosts.json"), nil)
	if err == nil || !strings.Contains(err.Error(), "not \"root\"") {
		t.Fatalf("whoami mismatch must refuse with the reason: %v", err)
	}
}
