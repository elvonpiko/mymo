package cli

import (
	"errors"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"

	"bytes"
	"context"
	"github.com/elvonpiko/mymo/internal/state"
	"reflect"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/sshtest"
)

// session is a CLI test session pinned to one HOME directory so that
// successive commands share the same local state.
type session struct{}

// newSession pins the home directory to a fresh directory for the
// current test. Windows resolves the home directory from
// USERPROFILE before it ever looks at HOME — both must name this
// session's directory, or every test shares one real profile and
// sees each other's nodes.
func newSession(t *testing.T) *session {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return &session{}
}

// run executes mymo within the session, returning exit code, stdout, stderr.
func (s *session) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	path, _ := sshtest.NewKey(t)
	return path
}

func TestNodeListEmpty(t *testing.T) {
	s := newSession(t)
	code, out, _ := s.run(t, "node", "list")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "No nodes yet") {
		t.Errorf("out = %q, want empty-fleet hint", out)
	}
}

func TestNodeAddWithFlagsAgentAuth(t *testing.T) {
	s := newSession(t)
	code, out, _ := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, `Added node "web-1"`) {
		t.Errorf("out = %q, want confirmation line", out)
	}
	code, out, _ = s.run(t, "node", "list")
	if code != exitOK || !strings.Contains(out, "web-1") {
		t.Errorf("list after add: code=%d out=%q", code, out)
	}
}

func TestNodeAddWithFlagsKeyAuth(t *testing.T) {
	s := newSession(t)
	key := writeTestKey(t)
	code, out, _ := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root",
		"-auth", "key", "-key", key)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, `Added node "web-1"`) {
		t.Errorf("out = %q, want confirmation line", out)
	}
}

func TestNodeAddDuplicateName(t *testing.T) {
	s := newSession(t)
	args := []string{"node", "add", "-name", "web-1", "-host", "203.0.113.10",
		"-user", "root", "-auth", "agent"}
	if code, _, _ := s.run(t, args...); code != exitOK {
		t.Fatalf("first add exit code = %d, want %d", code, exitOK)
	}
	code, _, errStr := s.run(t, args...)
	if code != exitErr {
		t.Fatalf("duplicate add exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "already exists") {
		t.Errorf("stderr = %q, want already-exists error", errStr)
	}
}

func TestNodeAddMissingFieldsWithoutTerminal(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "add", "-name", "web-1")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"missing required fields", "-host", "-user", "-auth"} {
		if !strings.Contains(errStr, want) {
			t.Errorf("stderr = %q, want it to mention %q", errStr, want)
		}
	}
}

func TestNodeAddInvalidName(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "add",
		"-name", "BAD", "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "name") {
		t.Errorf("stderr = %q, want invalid-name error", errStr)
	}
}

func TestNodeAddKeyFileMissing(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root",
		"-auth", "key", "-key", "/nonexistent/id_ed25519")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "key file") {
		t.Errorf("stderr = %q, want key-file error", errStr)
	}
}

func TestNodeInspect(t *testing.T) {
	s := newSession(t)
	if code, _, _ := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root",
		"-auth", "agent", "-port", "2222"); code != exitOK {
		t.Fatal("add failed")
	}
	code, out, _ := s.run(t, "node", "inspect", "web-1")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	for _, want := range []string{"web-1", "203.0.113.10", "2222", "root", "agent", "observe"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want it to contain %q", out, want)
		}
	}
}

func TestNodeInspectNotFound(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "inspect", "ghost")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "not found") {
		t.Errorf("stderr = %q, want not-found error", errStr)
	}
}

func TestNodeRemoveForced(t *testing.T) {
	s := newSession(t)
	add := []string{"node", "add", "-name", "web-1", "-host", "203.0.113.10",
		"-user", "root", "-auth", "agent"}
	if code, _, _ := s.run(t, add...); code != exitOK {
		t.Fatal("add failed")
	}
	// The -f flag deliberately comes after the positional argument.
	code, out, _ := s.run(t, "node", "rm", "web-1", "-f")
	if code != exitOK {
		t.Fatalf("rm exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, `Removed node "web-1"`) {
		t.Errorf("out = %q, want removal confirmation", out)
	}
	code, out, _ = s.run(t, "node", "list")
	if code != exitOK || !strings.Contains(out, "No nodes yet") {
		t.Errorf("list after rm: code=%d out=%q", code, out)
	}
}

func TestNodeRemoveMissing(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "rm", "ghost", "-f")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errStr, "not found") {
		t.Errorf("stderr = %q, want not-found error", errStr)
	}
}

func TestNodeRemoveRequiresConfirmationWithoutTerminal(t *testing.T) {
	s := newSession(t)
	add := []string{"node", "add", "-name", "web-1", "-host", "203.0.113.10",
		"-user", "root", "-auth", "agent"}
	if code, _, _ := s.run(t, add...); code != exitOK {
		t.Fatal("add failed")
	}
	code, _, errStr := s.run(t, "node", "rm", "web-1")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, "pass -f") {
		t.Errorf("stderr = %q, want -f hint", errStr)
	}
}

func TestNodeUnknownCommand(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "node", "bogus")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, `unknown node command "bogus"`) {
		t.Errorf("stderr = %q, want unknown-command error", errStr)
	}
}

func TestGatherFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		flags []string
		rest  []string
	}{
		{"flags first", []string{"-f", "web-1"}, []string{"-f", "web-1"}, nil},
		{"flags last", []string{"web-1", "-f"}, []string{"-f"}, []string{"web-1"}},
		{"flag with value", []string{"-name", "web-1"}, []string{"-name", "web-1"}, nil},
		{"mixed", []string{"web-1", "-name", "x", "-f"},
			[]string{"-name", "x", "-f"}, []string{"web-1"}},
		{"self contained", []string{"-name=x", "web-1"}, []string{"-name=x"}, []string{"web-1"}},
		{"dash value is its own flag", []string{"-key", "-foo"}, []string{"-key", "-foo"}, nil},
		{"only positionals", []string{"web-1", "web-2"}, nil, []string{"web-1", "web-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags, rest := gatherFlags(tc.args)
			if !reflect.DeepEqual(flags, tc.flags) {
				t.Errorf("flags = %v, want %v", flags, tc.flags)
			}
			if !reflect.DeepEqual(rest, tc.rest) {
				t.Errorf("rest = %v, want %v", rest, tc.rest)
			}
		})
	}
}

// node rm -f is the whole goodbye without a terminal: the node,
// its key pair, and the records of every application on it —
// locally only, nothing on any box. The goodbye line counts what
// was forgotten.
func TestNodeRemoveForgetsItsApps(t *testing.T) {
	s := newSession(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []domain.Node{
		{Name: "web-1", Host: "203.0.113.10", Port: 22, User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()},
		{Name: "web-2", Host: "203.0.113.11", Port: 22, User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()},
		{Name: "web-3", Host: "203.0.113.12", Port: 22, User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()},
	} {
		if err := store.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []domain.App{
		{Name: "api", Node: "web-1", Type: domain.SourceImage, Image: "nginx:1.27", Port: 8080},
		{Name: "keeper", Node: "web-2", Type: domain.SourceImage, Image: "nginx:1.27", Port: 80},
	} {
		if err := store.AddApp(a); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := s.run(t, "node", "rm", "web-1", "-f")
	if code != exitOK {
		t.Fatalf("exit code = %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "its 1 application record") {
		t.Errorf("the goodbye must count the forgotten records: %q", out)
	}
	if _, err := store.GetNode("web-1"); !errors.Is(err, state.ErrNodeNotFound) {
		t.Errorf("node survived removal: %v", err)
	}
	if _, err := store.GetApp("api"); !errors.Is(err, state.ErrAppNotFound) {
		t.Errorf("the app record survived its node's removal: %v", err)
	}
	if _, err := store.GetApp("keeper"); err != nil {
		t.Errorf("another node's app must survive: %v", err)
	}

	// a node with no apps keeps the plain goodbye
	code, out, errOut = s.run(t, "node", "rm", "web-3", "-f")
	if code != exitOK {
		t.Fatalf("exit code = %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "mymo forgot it; the box was not touched") {
		t.Errorf("a bare removal keeps the plain goodbye: %q", out)
	}
}
