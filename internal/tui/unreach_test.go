package tui

import (
	"strings"
	"testing"
)

// The unreachable page's moves: the record is one key away, the
// removal gate is the same dialog everywhere, and the goodbye
// lands the operator back on the list — the gone box's page is no
// place to stay.
func TestUnreachablePageOffersTheMoves(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n", "enter")
	m = step(t, m, checkDoneMsg{node: "web-1", err: errString("unreachable: connection refused"), seq: m.checkSeq})

	// i: the record is the honest place for what mymo last knew
	m = press(t, m, "i")
	if m.cur().kind != scNodeInspect {
		t.Fatalf("i did not open the record: %v", m.cur().kind)
	}
	m = press(t, m, "esc")
	if m.cur().kind != scNode {
		t.Fatalf("esc did not come back to the node page: %v", m.cur().kind)
	}

	// x opens the same removal gate as everywhere else
	m = press(t, m, "x")
	if !m.confirmRemove {
		t.Fatal("x did not open the removal gate")
	}
	if got := view(m); !strings.Contains(got, "not touched") {
		t.Fatalf("removal gate missing its promise:\n%s", got)
	}
	// esc keeps the box
	m = press(t, m, "esc")
	if m.confirmRemove || m.cur().kind != scNode {
		t.Fatalf("esc did not close the gate: confirm=%v screen=%v", m.confirmRemove, m.cur().kind)
	}

	// y forgets it: back to the fleet, the list no longer shows it
	m = press(t, m, "x")
	m = press(t, m, "y")
	if m.cur().kind != scFleet {
		t.Fatalf("removal from the page did not land on the fleet: %v", m.cur().kind)
	}
	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("the record survived its own removal")
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "the box was not touched") {
		t.Fatal("the goodbye toast missing its promise")
	}
}

// R on the fleet list opens the same removal gate — same dialog,
// same keys, same goodbye — and the list is where it ends.
func TestFleetRemoveShortcut(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	m := press(t, New(s), "n")

	m = press(t, m, "R")
	if !m.confirmRemove {
		t.Fatal("R did not open the removal gate from the fleet")
	}
	got := view(m)
	if !strings.Contains(got, "Remove web-1 from the fleet?") || !strings.Contains(got, "not touched") {
		t.Fatalf("the fleet gate is not the same dialog:\n%s", got)
	}
	// n keeps the box and the list
	m = press(t, m, "n")
	if m.confirmRemove || m.cur().kind != scFleet {
		t.Fatalf("n did not keep the box: confirm=%v screen=%v", m.confirmRemove, m.cur().kind)
	}

	// y forgets it: still on the fleet, the list refreshed
	m = press(t, m, "R")
	m = press(t, m, "y")
	if m.cur().kind != scFleet {
		t.Fatalf("removal left the fleet: %v", m.cur().kind)
	}
	if _, err := s.GetNode("web-1"); err == nil {
		t.Fatal("the record survived its own removal")
	}
	for _, it := range m.fleet.Items() {
		if item, ok := it.(fleetItem); ok && item.node.Name == "web-1" {
			t.Fatal("the fleet still lists the removed box")
		}
	}
}
