package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

func TestNodeAddPasswordRunsFirstContact(t *testing.T) {
	s := newSession(t)

	// the injected onboarding proves what it was handed
	var gotPassword, gotKeyPath, gotHost string
	var gotPort int
	origRun, origIn := cliFirstContact, nodePasswordIn
	t.Cleanup(func() { cliFirstContact, nodePasswordIn = origRun, origIn })
	cliFirstContact = func(ctx context.Context, host string, port int, user, password, keyPath, knownHostsPath string) error {
		gotHost, gotPort, gotPassword, gotKeyPath = host, port, password, keyPath
		// the real onboarding leaves a dedicated key behind; the
		// fake owes the flow the same world
		if _, err := ssh.GenerateEd25519(keyPath); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	nodePasswordIn = strings.NewReader("provider-secret\n")

	code, out, errOut := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root",
		"-port", "22", "-auth", "password")
	if code != exitOK {
		t.Fatalf("exit = %d\nstderr:[%s]\nstdout:[%s]", code, errOut, out)
	}
	if gotHost != "203.0.113.10" || gotPort != 22 || gotPassword != "provider-secret" {
		t.Fatalf("first contact ran with host=%q port=%d password=%q", gotHost, gotPort, gotPassword)
	}
	if !strings.Contains(out, "key-auth now — the password was never stored") {
		t.Errorf("stdout does not say what became of the password:\n%s", out)
	}
	if !strings.Contains(errOut, "first contact with root@203.0.113.10") {
		t.Errorf("stderr does not narrate the onboarding:\n%s", errOut)
	}

	// the saved node is key-auth with the dedicated key
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode("web-1")
	if err != nil {
		t.Fatalf("node not saved: %v", err)
	}
	if node.Auth != "key" {
		t.Fatalf("auth = %q, want key — the password is never a way of life", node.Auth)
	}
	home, _ := os.UserHomeDir()
	if want := ssh.FirstContactKeyPath(filepath.Join(home, ".mymo"), "web-1"); node.KeyPath != want {
		t.Fatalf("keyPath = %q, want %q", node.KeyPath, want)
	}
	if gotKeyPath != node.KeyPath {
		t.Fatalf("first contact installed to %q but the node says %q", gotKeyPath, node.KeyPath)
	}

	// the password is nowhere in the state directory
	assertNoPasswordOnDisk(t, "provider-secret")
}

func TestNodeAddPasswordFailureSavesNothing(t *testing.T) {
	s := newSession(t)

	origRun, origIn := cliFirstContact, nodePasswordIn
	t.Cleanup(func() { cliFirstContact, nodePasswordIn = origRun, origIn })
	cliFirstContact = func(context.Context, string, int, string, string, string, string) error {
		return errors.New("the box refused the password")
	}
	nodePasswordIn = strings.NewReader("wrong\n")

	code, _, errOut := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "password")
	if code != exitErr {
		t.Fatalf("exit = %d, want exitErr", code)
	}
	if !strings.Contains(errOut, "nothing was saved") {
		t.Errorf("stderr must say nothing was saved:\n%s", errOut)
	}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode("web-1"); err == nil {
		t.Fatal("a failed first contact must not save the node")
	}
	assertNoPasswordOnDisk(t, "wrong")
}

// TestNodeAddPasswordExpiredGuidesTheReset proves the CLI turns the
// forced reset into the operator's exact next move — no nameless
// exit 1.
func TestNodeAddPasswordExpiredGuidesTheReset(t *testing.T) {
	s := newSession(t)

	origRun, origIn := cliFirstContact, nodePasswordIn
	t.Cleanup(func() { cliFirstContact, nodePasswordIn = origRun, origIn })
	cliFirstContact = func(context.Context, string, int, string, string, string, string) error {
		return ssh.ErrPasswordExpired
	}
	nodePasswordIn = strings.NewReader("provider-secret\n")

	code, _, errOut := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "ubuntu", "-auth", "password")
	if code != exitErr {
		t.Fatalf("exit = %d, want exitErr", code)
	}
	for _, want := range []string{
		"demands a password change",
		"ssh ubuntu@203.0.113.10",
		"idempotent",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if _, err := state.Open(); err != nil {
		t.Fatal(err)
	}
	assertNoPasswordOnDisk(t, "provider-secret")
}

// assertNoPasswordOnDisk greps the state directory for the secret:
// the honest promise is that it exists nowhere persistent.
func assertNoPasswordOnDisk(t *testing.T, secret string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	_ = filepath.WalkDir(filepath.Join(home, ".mymo"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("the password leaked into %s", path)
		}
		return nil
	})
}
