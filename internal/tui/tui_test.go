package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
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
		m = step(t, m, keyMsg(k))
	}
	return m.(Model)
}

// step feeds one message to the model, then drains returned commands the
// way bubbletea's event loop would — but only while the embedded form is
// active, and keeping only huh's structural messages (field and group
// advances). Slow commands (cursor blink ticks, timers) are dropped so
// the driver never blocks on real-time waits.
func step(t *testing.T, m tea.Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	mm := next.(Model)
	if !mm.formActive() || cmd == nil {
		return mm
	}
	queue := []tea.Cmd{cmd}
	for i := 0; len(queue) > 0 && i < 64; i++ {
		c := queue[0]
		queue = queue[1:]

		res, ok := runCmdTimed(c, 20*time.Millisecond)
		if !ok {
			continue // timer-based command: irrelevant to the flow
		}
		switch r := res.(type) {
		case nil:
			// no message produced
		case tea.BatchMsg:
			queue = append(queue, r...)
		default:
			if !isHuhMsg(r) {
				continue
			}
			n2, c2 := mm.Update(r)
			mm = n2.(Model)
			if !mm.formActive() {
				return mm
			}
			if c2 != nil {
				queue = append(queue, c2)
			}
		}
	}
	return mm
}

// runCmdTimed executes a command and reports whether it finished within
// the timeout. Cursor blink and timer commands block on real time and are
// dropped instead.
func runCmdTimed(c tea.Cmd, d time.Duration) (tea.Msg, bool) {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case msg := <-ch:
		return msg, true
	case <-time.After(d):
		return nil, false
	}
}

// isHuhMsg reports whether a message is one of huh's internal messages.
func isHuhMsg(msg tea.Msg) bool {
	t := reflect.TypeOf(msg)
	return t != nil && t.PkgPath() == "charm.land/huh/v2"
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
	if got := view(mm); !strings.Contains(got, "add node") {
		t.Fatalf("workspace not entered after intro:\n%s", got)
	}
}

func TestIntroSkippedOnSecondRun(t *testing.T) {
	s := readyStore(t)
	m := New(s)
	if !m.introDone {
		t.Fatal("intro active on second run, want skipped")
	}
	if got := view(m); !strings.Contains(got, "add node") {
		t.Fatalf("second run should land on home:\n%s", got)
	}
}

func TestFleetEmptyState(t *testing.T) {
	m := press(t, New(readyStore(t)), "n")
	got := view(m)
	for _, want := range []string{"No servers yet", "[N]", "add your first VPS"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty fleet missing %q:\n%s", want, got)
		}
	}
}

func TestFleetShowsNodes(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	got := view(press(t, New(s), "n"))
	for _, want := range []string{"web-1", "web-2", "[observe]", "unchecked"} {
		if !strings.Contains(got, want) {
			t.Errorf("fleet missing %q:\n%s", want, got)
		}
	}
}

func TestNavigationStack(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	// entering observes: the loading page is up while the probe runs
	if got := view(m); !strings.Contains(got, "observing web-1") {
		t.Fatalf("enter did not start observation:\n%s", got)
	}
	m = step(t, m, checkDoneMsg{
		node:   "web-1",
		snap:   facts.Node{Hostname: "web-1", CollectedAt: time.Now()},
		client: ssh.New(domain.Node{}, ""),
		seq:    m.checkSeq,
	})
	if got := view(m); !strings.Contains(got, "Initial setup") {
		t.Fatalf("observe page did not open after the probe:\n%s", got)
	}
	m = press(t, m, "esc")
	if got := view(m); !strings.Contains(got, "NODES") {
		t.Fatalf("esc did not return to the fleet:\n%s", got)
	}
	m = press(t, m, "esc")
	if m.cur().kind != scHome {
		t.Fatalf("esc did not return home from the fleet: %v", m.cur().kind)
	}
}

func TestNodeActionsNavigation(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")

	// Actions in display order for an unmanaged box: Set up this
	// node, SSH, Applications, Inspect record, Remove. SSH execs a
	// real ssh client and Set up starts a real probe, so the journey
	// walks the navigable screens — after proving the setup action
	// is offered first.
	m := press(t, New(s), "n", "enter")
	m = step(t, m, checkDoneMsg{
		node: "web-1", seq: m.checkSeq,
		snap:   facts.Node{Hostname: "web-1", CollectedAt: time.Now()},
		client: ssh.New(domain.Node{}, ""),
	})
	if got := view(m); !strings.Contains(got, "Initial setup") {
		t.Fatalf("an unmanaged box must offer setup first:\n%s", got)
	}

	m = press(t, m, "down", "down", "enter") // action 2: Applications
	if got := view(m); !strings.Contains(got, "no mymo-managed applications") {
		t.Fatalf("applications action missing:\n%s", got)
	}
	m = press(t, m, "esc")
	if got := view(m); !strings.Contains(got, "Inspect record") {
		t.Fatalf("esc did not return to the observe page:\n%s", got)
	}

	m = press(t, m, "down", "enter") // action 3: Inspect (index preserved at 2)
	if got := view(m); !strings.Contains(got, "Stored record") {
		t.Fatalf("inspect action missing:\n%s", got)
	}
	m = press(t, m, "esc")

	m = press(t, m, "down", "enter") // action 4: Remove (index preserved at 3)
	if got := view(m); !strings.Contains(got, "not touched") {
		t.Fatalf("remove confirmation missing:\n%s", got)
	}
	m = press(t, m, "n") // cancel
	if got := view(m); !strings.Contains(got, "Inspect record") {
		t.Fatalf("n did not cancel removal:\n%s", got)
	}
}

func TestRemoveNodeFlow(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "web-2")
	m := press(t, New(s), "n", "enter")
	m = step(t, m, checkDoneMsg{
		node: "web-1", seq: m.checkSeq,
		snap:   facts.Node{Hostname: "web-1", CollectedAt: time.Now()},
		client: ssh.New(domain.Node{}, ""),
	})
	// five actions for an unmanaged box: Set up, SSH, Applications,
	// Inspect, Remove — four downs land on Remove.
	m = press(t, m, "down", "down", "down", "down", "enter", "y")
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
	m := press(t, New(s), "N")
	if m.cur().kind != scAddNode {
		t.Fatalf("N did not open the workflow: stack top = %v", m.cur().kind)
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
	m.addNode.vals = &addNodeValues{
		name:   "prod-01",
		host:   "203.0.113.10",
		port:   "22",
		user:   "root",
		choice: acAgent,
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
	m.addNode.vals = &addNodeValues{name: "BAD", host: "203.0.113.10", port: "22", user: "root", choice: acAgent}
	m.completeAddForm()
	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview", m.addNode.stage)
	}
	if m.addNode.err == "" {
		t.Fatal("validation error not surfaced")
	}
}

func TestAddNodeWorkflowTypedCompletion(t *testing.T) {
	s := readyStore(t)
	m := New(s)
	m = press(t, m, "N")

	// host
	m = press(t, m, "2", "0", "3", ".", "0", ".", "1", "1", "3", ".", "1", "0", "enter")
	// port (22 is prefilled)
	m = press(t, m, "enter")
	// user
	m = press(t, m, "r", "o", "o", "t", "enter")
	// name
	m = press(t, m, "p", "r", "o", "d", "-", "0", "1", "enter")
	// how to connect: default is password (first contact) — switch
	// to SSH agent, two options down, and accept
	m = press(t, m, "down", "down", "enter")
	// agent needs no further page: the form completes

	if m.addNode.stage != anReview {
		t.Fatalf("stage = %v, want anReview after completing the form", m.addNode.stage)
	}
	got := view(m)
	for _, want := range []string{"Review new node", "prod-01", "203.0.113.10"} {
		if !strings.Contains(got, want) {
			t.Errorf("review missing %q:\n%s", want, got)
		}
	}
	if m.addNode.node.Name != "prod-01" || m.addNode.node.Host != "203.0.113.10" {
		t.Fatalf("review node = %+v", m.addNode.node)
	}

	m = press(t, m, "c")
	stored, err := s.GetNode("prod-01")
	if err != nil {
		t.Fatalf("node not saved: %v", err)
	}
	if stored.User != "root" || stored.Auth != domain.AuthAgent {
		t.Fatalf("stored node = %+v", stored)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "prod-01") {
		t.Fatalf("no add toast: %+v", m.toast)
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
	m := press(t, New(s), "n", "/")
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
	m := press(t, New(s), "n", "?")
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

func TestHomeLandingAndKeys(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := New(s)
	if m.cur().kind != scHome {
		t.Fatalf("stack root = %v, want home", m.cur().kind)
	}
	if got := view(m); !strings.Contains(got, "add node") {
		t.Fatalf("home missing its key guide:\n%s", got)
	}
	// small n leads to the nodes page
	m = press(t, m, "n")
	if m.cur().kind != scFleet {
		t.Fatalf("n did not open the nodes page: %v", m.cur().kind)
	}
	// esc returns home
	m = press(t, m, "esc")
	if m.cur().kind != scHome {
		t.Fatalf("esc did not return home: %v", m.cur().kind)
	}
	// capital N opens the add-node workflow
	m = press(t, m, "N")
	if m.cur().kind != scAddNode {
		t.Fatalf("N did not open the add workflow: %v", m.cur().kind)
	}
	// aborting the workflow lands on the fleet beneath it
	m = press(t, m, "ctrl+c")
	if m.cur().kind != scFleet {
		t.Fatalf("abort did not land on the fleet: %v", m.cur().kind)
	}
}

func TestDescriptionTypewriter(t *testing.T) {
	m := New(readyStore(t))
	if !m.descAnim {
		t.Fatal("typewriter not running on landing")
	}
	next, _ := m.Update(descTickMsg{})
	m = next.(Model)
	if m.descShown == 0 {
		t.Fatal("first tick revealed nothing")
	}
	for m.descAnim {
		next, _ := m.Update(descTickMsg{})
		m = next.(Model)
	}
	if got := view(m); !strings.Contains(got, "local-first, from one terminal.") {
		t.Fatalf("full description missing:\n%s", got)
	}
}

func TestEmptyFleetShowsDescriptionAndShortcuts(t *testing.T) {
	s := readyStore(t)
	m := press(t, New(s), "n")
	if !m.descAnim {
		t.Fatal("empty fleet did not restart the typewriter")
	}
	for m.descAnim {
		next, _ := m.Update(descTickMsg{})
		m = next.(Model)
	}
	got := view(m)
	for _, want := range []string{
		"opinionated window onto your fleet:",
		"No servers yet",
		"esc", "home",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("empty fleet missing %q:\n%s", want, got)
		}
	}
}

func TestHomeIsChromeless(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := New(s)
	got := view(m)
	// the mark renders two half-blocks per appearance: home shows it
	// once, in its content — a second appearance would mean the header
	if n := strings.Count(got, "▀"); n != 2 {
		t.Fatalf("home shows the mark %d times, want once:\n%s", n/2, got)
	}
	m = press(t, m, "n")
	if got := view(m); !strings.Contains(got, "fleet") {
		t.Fatalf("subpage lost the sticky header breadcrumb:\n%s", got)
	}
}

func TestRunRequiresTerminal(t *testing.T) {
	if err := Run(testStore(t)); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("Run() = %v, want ErrNoTerminal", err)
	}
}

func TestFleetListRidesInACard(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedNode(t, s, "db-1")
	m := press(t, New(s), "n")

	lines := strings.Split(ansiStrip(view(m)), "\n")
	// the list carries the card title like the observe page's cards
	if !strings.Contains(lines[3], "NODES") {
		t.Fatalf("fleet list not carded:\n%s", strings.Join(lines, "\n"))
	}
	// card corners align: the title border spans the card
	colOf := func(line, marker string) int {
		i := strings.Index(line, marker)
		if i < 0 {
			return -1
		}
		return utf8.RuneCountInString(line[:i])
	}
	// find the card's own top and bottom rows — not the frame's
	top, bottom := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "NODES") && strings.Contains(l, "╭") {
			top = i
			break
		}
	}
	if top < 0 {
		t.Fatalf("fleet card title missing:\n%s", strings.Join(lines, "\n"))
	}
	for i := top + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "╯") {
			bottom = i
			break
		}
	}
	if bottom < 0 {
		t.Fatalf("fleet card bottom border missing:\n%s", strings.Join(lines, "\n"))
	}
	if gotTop, gotBot := colOf(lines[top], "╮"), colOf(lines[bottom], "╯"); gotTop != gotBot {
		t.Errorf("fleet card corners misaligned: top=%d bottom=%d", gotTop, gotBot)
	}
	// the applications summary survives the floor budget below the card
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "APPLICATIONS") || !strings.Contains(joined, "none managed yet") {
		t.Errorf("applications section lost below the card:\n%s", joined)
	}
}
