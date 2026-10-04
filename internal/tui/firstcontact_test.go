package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// firstContactModel builds a model parked at the review step with the
// password choice made, the way the wizard produces it.
func firstContactModel(t *testing.T) (Model, *state.Store) {
	t.Helper()
	s := readyStore(t)
	m := New(s)
	m.addNode.vals = &addNodeValues{
		name: "web-1", host: "203.0.113.10", port: "22",
		user: "root", choice: acPassword, password: "provider-secret",
	}
	m.push(screen{kind: scAddNode})
	m.completeAddForm()
	if m.addNode.stage != anReview || !m.addNode.firstContact {
		t.Fatalf("stage = %v, firstContact = %v", m.addNode.stage, m.addNode.firstContact)
	}
	if m.addNode.node.Auth != domain.AuthKey {
		t.Fatalf("password choice must map to a key-auth record, got %q", m.addNode.node.Auth)
	}
	if want := ssh.FirstContactKeyPath(s.Dir(), "web-1"); m.addNode.node.KeyPath != want {
		t.Fatalf("keyPath = %q, want %q", m.addNode.node.KeyPath, want)
	}
	return m, s
}

// settleConfirm runs the confirm command the way bubbletea would:
// the spinner tick blocks on real time and is dropped; the first
// contact settles and its done message lands on the model.
func settleConfirm(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg, ok := runCmdTimed(cmd, time.Second)
	if !ok {
		t.Fatal("the confirm command never settled")
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("confirm produced %T, want a batch", msg)
	}
	for _, c := range batch {
		if sub, ok := runCmdTimed(c, 50*time.Millisecond); ok {
			if done, ok := sub.(firstContactDoneMsg); ok {
				next, _ := m.Update(done)
				return next.(Model)
			}
		}
	}
	t.Fatal("the first-contact command never settled")
	return m
}

func TestFirstContactConfirmSavesKeyAuthNode(t *testing.T) {
	orig := firstContactRun
	t.Cleanup(func() { firstContactRun = orig })
	firstContactRun = func(host string, port int, user, password, keyPath, knownHostsPath string) error {
		if password != "provider-secret" {
			t.Errorf("first contact ran with password %q", password)
		}
		// the real onboarding leaves a dedicated key behind
		if _, err := ssh.GenerateEd25519(keyPath); err != nil {
			t.Fatal(err)
		}
		return nil
	}

	m, s := firstContactModel(t)
	next, cmd := m.updateAddReview("c")
	m = next.(Model)
	if m.addNode.stage != anInstalling {
		t.Fatalf("stage = %v, want anInstalling", m.addNode.stage)
	}
	m = settleConfirm(t, m, cmd)

	node, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("node not saved: %v", err)
	}
	if node.Auth != domain.AuthKey {
		t.Fatalf("saved auth = %q, want key", node.Auth)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "key-auth now") {
		t.Fatalf("toast must say what changed: %+v", m.toast)
	}
	// the values copy went out of scope with the ceremony
	if m.cur().kind == scAddNode {
		t.Fatalf("workflow did not close: stack top = %v", m.cur().kind)
	}
}

func TestFirstContactFailureShowsTheErrorAndSavesNothing(t *testing.T) {
	orig := firstContactRun
	t.Cleanup(func() { firstContactRun = orig })
	firstContactRun = func(string, int, string, string, string, string) error {
		return errors.New("the box refused the password")
	}

	m, s := firstContactModel(t)
	next, cmd := m.updateAddReview("c")
	m = settleConfirm(t, next.(Model), cmd)

	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("a failed first contact must not save the node")
	}
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview after failure", m.addNode.stage)
	}
	if !strings.Contains(m.addNode.err, "refused the password") {
		t.Fatalf("the review must say what went wrong: %q", m.addNode.err)
	}
	if got := view(m); !strings.Contains(got, "refused the password") {
		t.Fatalf("the error is not rendered:\n%s", got)
	}
}

func TestFirstContactEscDuringInstallCancels(t *testing.T) {
	m, s := firstContactModel(t)
	next, _ := m.updateAddReview("c")
	m = next.(Model)

	mm, _ := m.updateInstalling("esc")
	m = mm.(Model)
	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("a cancelled first contact must not save the node")
	}
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	if !strings.Contains(m.addNode.err, "cancelled") {
		t.Fatalf("the review must say it was cancelled: %q", m.addNode.err)
	}
}
