package apply

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/sshtest"
)

// TestProveMymoKeyAcceptsTheRealTerminalShape is the live-fire gate
// regression: on the first real box, the identity check compared
// "mymo\n" to "mymo" and refused every healthy node — the fakes had
// always answered without the newline. A real box's answer is proof,
// not a mismatch.
func TestProveMymoKeyAcceptsTheRealTerminalShape(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	// the handler answers exactly as a real id -un does: identity
	// plus the terminal's newline
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		if cmd == "id -un" {
			return "mymo\n", 0
		}
		return "", 1
	})
	dir := t.TempDir()
	p := SSHProver{
		Node:       srv.Node(t, keyPath),
		PrivateKey: keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts.json"),
	}
	if err := p.ProveMymoKey(context.Background()); err != nil {
		t.Fatalf("ProveMymoKey() = %v, want nil — a real box answers with a trailing newline", err)
	}
}

// TestProveMymoKeyRefusesAWrongIdentity proves the check still has
// teeth: the key accepted, the identity not what mymo expects.
func TestProveMymoKeyRefusesAWrongIdentity(t *testing.T) {
	keyPath, _ := sshtest.NewKey(t)
	srv := sshtest.NewServer(t, func(cmd string) (string, int) {
		if cmd == "id -un" {
			return "someone-else\n", 0
		}
		return "", 1
	})
	dir := t.TempDir()
	p := SSHProver{
		Node:       srv.Node(t, keyPath),
		PrivateKey: keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts.json"),
	}
	err := p.ProveMymoKey(context.Background())
	if err == nil {
		t.Fatal("ProveMymoKey() = nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "the key was accepted but the identity check failed") {
		t.Fatalf("refusal copy = %q", err.Error())
	}
}
