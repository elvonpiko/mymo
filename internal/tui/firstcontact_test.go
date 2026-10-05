package tui

import (
	"context"
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
	firstContactRun = func(ctx context.Context, host string, port int, user, password, keyPath, knownHostsPath string, progress func(string)) error {
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
	firstContactRun = func(context.Context, string, int, string, string, string, string, func(string)) error {
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

// TestFirstContactExpiredPasswordSaysTheMove proves the review names
// the forced reset as the box's condition with the operator's next
// step — a one-line move, no nameless failure.
func TestFirstContactExpiredPasswordSaysTheMove(t *testing.T) {
	orig := firstContactRun
	t.Cleanup(func() { firstContactRun = orig })
	firstContactRun = func(context.Context, string, int, string, string, string, string, func(string)) error {
		return ssh.ErrPasswordExpired
	}

	m, s := firstContactModel(t)
	next, cmd := m.updateAddReview("c")
	m = settleConfirm(t, next.(Model), cmd)

	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("an expired password must not save the node")
	}
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	if !strings.Contains(m.addNode.err, "password change forced") {
		t.Fatalf("the review must name the move: %q", m.addNode.err)
	}
	got := view(m)
	if !strings.Contains(got, "ssh in once, change it") {
		t.Fatalf("the move is not rendered:\n%s", got)
	}
}

// TestFirstContactRetryLinesLandInTheCard proves the wiring end to
// end: the ceremony hands the runner a live progress func, a retry
// line travels the pump, and the handler paints it on the loading
// card while the work is still in flight.
func TestFirstContactRetryLinesLandInTheCard(t *testing.T) {
	var gotProgress func(string)
	orig := firstContactRun
	t.Cleanup(func() { firstContactRun = orig })
	firstContactRun = func(ctx context.Context, host string, port int, user, password, keyPath, knownHostsPath string, progress func(string)) error {
		gotProgress = progress
		progress("still connecting to " + host + " — try 2 of 3 (i/o timeout)")
		if _, err := ssh.GenerateEd25519(keyPath); err != nil {
			t.Fatal(err)
		}
		return nil
	}

	m, s := firstContactModel(t)
	next, cmd := m.updateAddReview("c")
	m = next.(Model)

	// while the work is in flight, the handler paints the line
	inFlight, _ := m.Update(netProgressMsg{line: "still connecting to 203.0.113.10 — try 2 of 3 (i/o timeout)"})
	if mm := inFlight.(Model); mm.loading.detail != "still connecting to 203.0.113.10 — try 2 of 3 (i/o timeout)" {
		t.Fatalf("the card did not take the retry line: %q", mm.loading.detail)
	}

	m = settleConfirm(t, m, cmd)
	if gotProgress == nil {
		t.Fatal("the ceremony never handed the runner a progress func")
	}
	// the runner pushed its line through that func; the handler test
	// above proves the same line paints the card while in flight
	if _, err := s.GetNode("web-1"); err != nil {
		t.Fatalf("node not saved: %v", err)
	}
}

// TestFirstContactEscCancelsTheWork proves esc stops the onboarding
// itself — the runner's context ends — and a late result from the
// cancelled run can never save the node.
func TestFirstContactEscCancelsTheWork(t *testing.T) {
	ctxEnded := make(chan struct{})
	orig := firstContactRun
	t.Cleanup(func() { firstContactRun = orig })
	firstContactRun = func(ctx context.Context, host string, port int, user, password, keyPath, knownHostsPath string, progress func(string)) error {
		<-ctx.Done()
		close(ctxEnded)
		return ctx.Err()
	}

	m, s := firstContactModel(t)
	next, cmd := m.updateAddReview("c")
	m = next.(Model)

	// the runner starts for real and parks on its context: the way
	// the runtime does it, every batched command runs in its own
	// goroutine — a batch is concurrency, not a queue
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			go func(c tea.Cmd) { _ = c() }(c)
		}
	}
	mm, _ := m.updateInstalling("esc")
	m = mm.(Model)
	select {
	case <-ctxEnded:
	case <-time.After(time.Second):
		t.Fatal("esc did not cancel the runner's context")
	}
	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("a cancelled first contact must not save the node")
	}

	// the late result from the cancelled run is dropped, not applied
	after, _ := m.Update(firstContactDoneMsg{err: context.Canceled})
	m = after.(Model)
	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("a late result saved a node its operator cancelled")
	}
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	if !strings.Contains(m.addNode.err, "cancelled") {
		t.Fatalf("the esc note was clobbered: %q", m.addNode.err)
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
