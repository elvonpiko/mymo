package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
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

func TestFleetEmptyState(t *testing.T) {
	m := New(testStore(t))
	if content := m.View().Content; !strings.Contains(content, "No nodes yet") {
		t.Errorf("View() = %q, want empty-fleet hint", content)
	}
}

func TestFleetShowsNodes(t *testing.T) {
	s := testStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := New(s)
	content := m.View().Content
	for _, want := range []string{"web-1", "web-2", "[observe]"} {
		if !strings.Contains(content, want) {
			t.Errorf("View() missing %q:\n%s", want, content)
		}
	}
}

func TestQuitCommand(t *testing.T) {
	m := New(testStore(t))
	_, cmd := m.Update(tea.KeyPressMsg{Text: "q"})
	if cmd == nil {
		t.Fatal("pressing q returned no command, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("pressing q returned %T, want tea.QuitMsg", cmd())
	}
}

func TestTypingQInFilterDoesNotQuit(t *testing.T) {
	s := testStore(t)
	seedNode(t, s, "web-1")
	m := New(s)

	m2, _ := m.Update(tea.KeyPressMsg{Text: "/"})
	filtering := m2.(Model)
	if filtering.fleet.FilterState() != list.Filtering {
		t.Fatalf("after / filter state = %v, want Filtering", filtering.fleet.FilterState())
	}

	_, cmd := filtering.Update(tea.KeyPressMsg{Text: "q"})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("pressing q while typing a filter quit the program")
		}
	}
}

func TestOpenNodeDetail(t *testing.T) {
	s := testStore(t)
	n := seedNode(t, s, "web-1")
	m := New(s)

	detail, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	content := detail.(Model).View().Content
	for _, want := range []string{"web-1", n.Host, n.User, "added"} {
		if !strings.Contains(content, want) {
			t.Errorf("node view missing %q:\n%s", want, content)
		}
	}

	back, _ := detail.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if content := back.(Model).View().Content; !strings.Contains(content, "fleet") {
		t.Errorf("after esc view = %q, want fleet screen", content)
	}
}

func TestLoadErrorShown(t *testing.T) {
	s := testStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), "nodes.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(s)
	if content := m.View().Content; !strings.Contains(content, "failed to load state") {
		t.Errorf("View() = %q, want load error surfaced", content)
	}
}

func TestRunRequiresTerminal(t *testing.T) {
	if err := Run(testStore(t)); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("Run() = %v, want ErrNoTerminal", err)
	}
}
