package tui

import (
	"strings"
	"testing"
)

// h is home from anywhere: the same way back on every page, no
// matter how deep the stack — except while a form owns the keys or
// a ceremony is running.
func TestHomeKeyReturnsFromEverywhere(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := New(s)
	m.introDone = true

	// from the fleet
	m = press(t, m, "n")
	if m.cur().kind != scFleet {
		t.Fatalf("not on the fleet: %v", m.cur().kind)
	}
	m = press(t, m, "h")
	if m.cur().kind != scHome {
		t.Fatalf("h did not come home from the fleet: %v", m.cur().kind)
	}
	if strings.Contains(view(m), "fleet / web-1") {
		t.Fatal("the stack leaked into the home page")
	}

	// from deep inside a node — its applications screen
	m = press(t, m, "n", "enter")
	m = observe(t, m, "web-1", richSnapshot("web-1"))
	m = press(t, m, "down", "down", "enter") // open Applications
	if m.cur().kind != scNodeApps {
		t.Fatalf("not on the apps screen: %v", m.cur().kind)
	}
	m = press(t, m, "h")
	if m.cur().kind != scHome {
		t.Fatalf("h did not come home from the apps screen: %v", m.cur().kind)
	}

	// the home page still works afterwards — h is a clean cut, not
	// a broken stack
	m = press(t, m, "n")
	if m.cur().kind != scFleet {
		t.Fatalf("home is broken after h: %v", m.cur().kind)
	}
}

// while the plan page's input owns the keyboard, h is text — a node
// name like "ts-hrly-srv-01" must type in full, never teleport home
func TestHomeKeyStandsDownWhileTyping(t *testing.T) {
	runner := &cannedApplyRunner{}
	m := atPlanScreen(t, runner, fakeTUIProver{})

	m = press(t, m, "t", "s", "-", "h")
	if m.cur().kind != scNodePlan {
		t.Fatalf("h teleported home from the input: %v", m.cur().kind)
	}
	if m.applyTyped != "ts-h" {
		t.Fatalf("the input lost its runes: %q", m.applyTyped)
	}
	// ? and q are text here too, not help and quit
	m = press(t, m, "r", "l", "y")
	if m.helpOpen {
		t.Fatal("? opened help from inside the input")
	}
	if m.applyTyped != "ts-hrly" {
		t.Fatalf("the input lost its runes: %q", m.applyTyped)
	}
	// "ts-hrly" is seven runes; five backspaces leave "ts"
	m = press(t, m, "backspace", "backspace", "backspace", "backspace", "backspace")
	if m.applyTyped != "ts" {
		t.Fatalf("backspace did not edit: %q", m.applyTyped)
	}
	m = press(t, m, "backspace", "backspace")
	if m.applyTyped != "" {
		t.Fatalf("the buffer did not empty: %q", m.applyTyped)
	}
}
