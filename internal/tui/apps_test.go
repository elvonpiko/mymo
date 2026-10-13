package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
)

func seedApp(t *testing.T, s *state.Store, a domain.App) {
	t.Helper()
	if err := s.AddApp(a); err != nil {
		t.Fatal(err)
	}
}

func sampleApp(name, node string) domain.App {
	return domain.App{
		Name: name, Node: node, Type: domain.SourceImage,
		Image: "ghcr.io/x/" + name + ":1.2.3", Port: 8080,
		Domain: name + ".example.com", Health: "/healthz",
		Releases: []domain.Release{
			{ID: 1, State: domain.ReleaseRetired, Container: "mymo-" + name + "-r1",
				Digest: "sha256:aaaa1111aaaa", Health: "ok", DeployedAt: time.Now().Add(-2 * time.Hour)},
			{ID: 2, State: domain.ReleaseActive, Container: "mymo-" + name + "-r2",
				Digest: "sha256:bbbb2222bbbb", Health: "ok", DeployedAt: time.Now()},
		},
	}
}

func TestAppsPageListsTheRecord(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	seedApp(t, s, func() domain.App {
		a := sampleApp("worker", "web-1")
		a.Domain, a.Health, a.Releases = "", "", nil
		return a
	}())

	m := press(t, New(s), "a")
	out := ansiStrip(view(m))
	for _, want := range []string{"Applications", "api", "worker", "api.example.com", "internal", "r2 live", "no releases"} {
		if !strings.Contains(out, want) {
			t.Errorf("apps page missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "> ") != 1 {
		t.Errorf("cursor rendered more than once:\n%s", out)
	}

	// the footer counts the fleet honestly
	if !strings.Contains(out, "1 node · 2 applications") {
		t.Errorf("footer count wrong:\n%s", out)
	}
}

func TestAppsPageIsEmptyHonest(t *testing.T) {
	s := readyStore(t)
	m := press(t, New(s), "a")
	out := ansiStrip(view(m))
	if !strings.Contains(out, "No applications are managed yet") ||
		!strings.Contains(out, "mymo deploy") {
		t.Errorf("empty page:\n%s", out)
	}
}

func TestAppsEnterOpensTheDetail(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))

	m := press(t, New(s), "a")
	m, _ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	out := ansiStrip(view(m))
	for _, want := range []string{"api", "https://api.example.com", "kept", "r2"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail page missing %q:\n%s", want, out)
		}
	}
	// the page is key-first now: the verbs sit in the footer as
	// keys — rollback appears because r1 is kept — and their
	// words live in the keyboard card
	for _, verb := range []string{"s status", "l logs", "r restart", "b rollback", "e shell"} {
		if !strings.Contains(out, verb) {
			t.Errorf("detail page footer missing the %q verb:\n%s", verb, out)
		}
	}
	m = press(t, m, "?")
	out = ansiStrip(view(m))
	for _, verb := range []string{"Live status", "Logs", "Restart", "Rollback", "Shell", "quit"} {
		if !strings.Contains(out, verb) {
			t.Errorf("the keyboard card is missing the %q verb:\n%s", verb, out)
		}
	}
	m = press(t, m, "x") // any key closes the card

	// esc returns to the list
	m, _ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(ansiStrip(view(m)), "Applications") {
		t.Errorf("esc did not return to the list:\n%s", ansiStrip(view(m)))
	}
}

func TestNodeAppsPageFiltersToOneNode(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	seedApp(t, s, sampleApp("api", "web-1"))
	seedApp(t, s, sampleApp("celery", "web-2"))

	// the applications verb exists only where mymo set the box up:
	// make web-1 a managed node, then the probe answers and the
	// page's a key opens its applications
	if n, err := s.GetNode("web-1"); err == nil {
		n.Bootstrap.State = domain.BootstrapReady
		if err := s.UpdateNode(n); err != nil {
			t.Fatal(err)
		}
	}
	m := press(t, New(s), "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = press(t, m, "a")
	out := ansiStrip(view(m))
	if !strings.Contains(out, "api") {
		t.Errorf("web-1's apps missing api:\n%s", out)
	}
	if strings.Contains(out, "celery") {
		t.Errorf("another node's app leaked in:\n%s", out)
	}
}

func TestAppDetailShowsFailureHistory(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	a := sampleApp("api", "web-1")
	a.Releases = []domain.Release{
		{ID: 1, State: domain.ReleaseFailed, Container: "mymo-api-r1",
			Health: "failed: /healthz never answered", DeployedAt: time.Now()},
	}
	seedApp(t, s, a)

	m := press(t, New(s), "a")
	m, _ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	out := ansiStrip(view(m))
	if !strings.Contains(out, "failed") || !strings.Contains(out, "never answered") {
		t.Errorf("failure history not honest:\n%s", out)
	}
}

// driveKeys feeds messages one at a time, discarding produced
// messages — plain navigation returns no real commands.
func driveKeys(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		m, _ = drive(t, m, msg)
	}
	return m
}
