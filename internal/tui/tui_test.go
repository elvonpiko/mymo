package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
)

func testStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.OpenDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDir() = %v, want nil", err)
	}
	return s
}

// readyStore returns a store whose first-run intro has already been seen.
func readyStore(t *testing.T) *state.Store {
	t.Helper()
	s := testStore(t)
	if err := s.SaveMeta(state.MetaDoc{IntroSeen: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

func seedNode(t *testing.T, s *state.Store, name string) domain.Node {
	t.Helper()
	n := domain.Node{
		Name:    name,
		Host:    "203.0.113.10",
		Port:    domain.DefaultSSHPort,
		User:    "root",
		Auth:    domain.AuthAgent,
		Mode:    domain.ModeObserve,
		AddedAt: time.Now(),
	}
	if err := s.AddNode(n); err != nil {
		t.Fatalf("AddNode() = %v, want nil", err)
	}
	return n
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Text: s, Code: rune(s[0])}
	}
}

// press sends keys to a model and returns the resulting Model.
func press(t *testing.T, m tea.Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(keyMsg(k))
		m = next
	}
	mm, ok := m.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", m)
	}
	return mm
}

func view(m tea.Model) string {
	return m.(Model).View().Content
}

func TestFirstRunShowsIntro(t *testing.T) {
	s := testStore(t)
	m := New(s)
	if m.introDone {
		t.Fatal("introDone = true on first run, want false")
	}
	if got := view(m); !strings.Contains(got, "mymo") {
		t.Fatalf("intro view missing brand:\n%s", got)
	}
	// the animation reveals the rest over ticks
	for i := 0; i < introSteps; i++ {
		next, _ := m.Update(introTickMsg{})
		m = next.(Model)
	}
	if got := view(m); !strings.Contains(got, "press any key") {
		t.Fatalf("intro view missing prompt:\n%s", got)
	}
	// first key skips the animation, second enters the workspace
	mm := press(t, m, "x")
	if !mm.introDone {
		t.Fatal("intro still active after skip+enter keys")
	}
	meta, err := s.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if !meta.IntroSeen {
		t.Fatal("intro completion not persisted to meta")
	}
	if got := view(mm); !strings.Contains(got, "fleet") {
		t.Fatalf("workspace not entered after intro:\n%s", got)
	}
}

func TestIntroSkippedOnSecondRun(t *testing.T) {
	s := readyStore(t)
	m := New(s)
	if !m.introDone {
		t.Fatal("intro active on second run, want skipped")
	}
	if got := view(m); !strings.Contains(got, "No servers yet") {
		t.Fatalf("second run should land on the fleet:\n%s", got)
	}
}

func TestReplayIntroFromSettings(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "s", "i")
	if m.introDone {
		t.Fatal("replay did not restart the intro")
	}
	if got := view(m); !strings.Contains(got, "mymo") {
		t.Fatalf("intro not rendered:\n%s", got)
	}
}

func TestFleetEmptyState(t *testing.T) {
	m := New(readyStore(t))
	got := view(m)
	for _, want := range []string{"No servers yet", "add your first VPS"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty fleet missing %q:\n%s", want, got)
		}
	}
}

func TestFleetShowsNodes(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	got := view(New(s))
	for _, want := range []string{"web-1", "web-2", "[observe]", "unverified"} {
		if !strings.Contains(got, want) {
			t.Errorf("fleet missing %q:\n%s", want, got)
		}
	}
}

func TestNavigationStack(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "enter")
	if got := view(m); !strings.Contains(got, "ACTIONS") {
		t.Fatalf("enter did not open the node screen:\n%s", got)
	}
	m = press(t, m, "esc")
	if got := view(m); !strings.Contains(got, "NODES") {
		t.Fatalf("esc did not return to the fleet:\n%s", got)
	}
}

func TestNodeActionsNavigation(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")

	m := press(t, New(s), "enter", "enter") // action 0: Applications
	if got := view(m); !strings.Contains(got, "no mymo-managed applications") {
		t.Fatalf("applications action missing:\n%s", got)
	}
	m = press(t, m, "esc") // back to node
	if got := view(m); !strings.Contains(got, "ACTIONS") {
		t.Fatalf("esc did not return to the node screen:\n%s", got)
	}

	m = press(t, m, "down", "enter") // action 1: Inspect
	if got := view(m); !strings.Contains(got, "Stored record") {
		t.Fatalf("inspect action missing:\n%s", got)
	}
	m = press(t, m, "esc")

	m = press(t, m, "down", "enter") // action 2: SSH (list index preserved at 1)
	if got := view(m); !strings.Contains(got, "ssh -p 22 root@203.0.113.10") {
		t.Fatalf("ssh action missing manual command:\n%s", got)
	}
	m = press(t, m, "esc")

	m = press(t, m, "down", "enter") // action 3: Remove (index preserved at 2)
	if got := view(m); !strings.Contains(got, "not touched") {
		t.Fatalf("remove confirmation missing:\n%s", got)
	}
	m = press(t, m, "n") // cancel
	if got := view(m); !strings.Contains(got, "ACTIONS") {
		t.Fatalf("n did not cancel removal:\n%s", got)
	}
}

func TestRemoveNodeFlow(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := press(t, New(s), "enter", "down", "down", "down", "enter", "y")
	if _, err := s.GetNode("web-1"); !errors.Is(err, state.ErrNodeNotFound) {
		t.Fatalf("node still present after confirm: %v", err)
	}
	if got := view(m); !strings.Contains(got, "web-2") {
		t.Fatalf("fleet after removal missing web-2:\n%s", got)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "web-1") {
		t.Fatalf("no removal toast: %+v", m.toast)
	}
}

func TestAddNodeWorkflowCancel(t *testing.T) {
	s := readyStore(t)
	m := press(t, New(s), "n")
	if m.cur().kind != scAddNode {
		t.Fatalf("n did not open the workflow: stack top = %v", m.cur().kind)
	}
	if got := view(m); !strings.Contains(got, "Name") {
		t.Fatalf("workflow form not rendered:\n%s", got)
	}
	m = press(t, m, "ctrl+c") // abort the embedded form
	if m.cur().kind == scAddNode {
		t.Fatalf("form abort did not cancel the workflow: stack top = %v", m.cur().kind)
	}
	if got := view(m); !strings.Contains(got, "No servers yet") {
		t.Fatalf("form abort did not return to the fleet:\n%s", got)
	}
}

func TestAddNodeReviewSavesNode(t *testing.T) {
	s := readyStore(t)
	m := New(s)
	m.addNode.vals = addNodeValues{
		name: "prod-01",
		host: "203.0.113.10",
		port: "22",
		user: "root",
		auth: domain.AuthAgent,
	}
	m.push(screen{kind: scAddNode})
	m.completeAddForm()
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	next, _ := m.updateAddReview("c")
	m = next.(Model)
	got, err := s.GetNode("prod-01")
	if err != nil {
		t.Fatalf("node not saved: %v", err)
	}
	if got.Mode != domain.ModeObserve {
		t.Fatalf("mode = %q, want observe", got.Mode)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "prod-01") {
		t.Fatalf("no add toast: %+v", m.toast)
	}
	if gotView := view(m); !strings.Contains(gotView, "prod-01") {
		t.Fatalf("fleet does not show the new node:\n%s", gotView)
	}
}

func TestAddNodeReviewValidationFailure(t *testing.T) {
	m := New(readyStore(t))
	m.addNode.vals = addNodeValues{name: "BAD", host: "203.0.113.10", port: "22", user: "root", auth: domain.AuthAgent}
	m.completeAddForm()
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	if m.addNode.err == "" {
		t.Fatal("validation error not surfaced")
	}
}

func TestQuitFromFleet(t *testing.T) {
	m := New(readyStore(t))
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q returned %T, want tea.QuitMsg", cmd())
	}
}

func TestTypingQInFilterDoesNotQuit(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "/")
	if !m.filterActive() {
		t.Fatal("/ did not activate the fleet filter")
	}
	next, cmd := m.Update(keyMsg("q"))
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q while filtering quit the workspace")
		}
	}
	if !next.(Model).filterActive() {
		t.Fatal("q did not reach the filter")
	}
}

func TestHelpOverlay(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "?")
	if got := view(m); !strings.Contains(got, "Keyboard help") {
		t.Fatalf("? did not open the help overlay:\n%s", got)
	}
	m = press(t, m, "x") // any key closes
	if got := view(m); !strings.Contains(got, "NODES") {
		t.Fatalf("help overlay did not close:\n%s", got)
	}
}

func TestToastExpiry(t *testing.T) {
	s := readyStore(t)
	m := New(s)
	m.toast = &toast{id: 42, text: "hello", kind: toastOK}
	next, _ := m.Update(toastExpiredMsg{id: 42})
	if next.(Model).toast != nil {
		t.Fatal("toast survived its expiry")
	}
}

func TestSettingsScreen(t *testing.T) {
	m := press(t, New(readyStore(t)), "s")
	if got := view(m); !strings.Contains(got, "Settings") {
		t.Fatalf("s did not open settings:\n%s", got)
	}
}

func TestLoadErrorSurfaces(t *testing.T) {
	s := readyStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), "nodes.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := view(New(s)); !strings.Contains(got, "could not read its local state") {
		t.Fatalf("load error not surfaced:\n%s", got)
	}
}

func TestRunRequiresTerminal(t *testing.T) {
	if err := Run(testStore(t)); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("Run() = %v, want ErrNoTerminal", err)
	}
}
