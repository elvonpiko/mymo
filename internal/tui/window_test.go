package tui

import "testing"

func TestScrollMaxHeightsKeepsTheLastRowWhole(t *testing.T) {
	// three rows of height 2, window of 4: the last start that fits
	// two rows is index 1 (rows 1 and 2). Index 0 would also fit but
	// is not the maximum.
	if got := scrollMaxHeights([]int{2, 2, 2}, 4); got != 1 {
		t.Fatalf("scroll max = %d, want 1", got)
	}
	if got := scrollMaxHeights([]int{1, 1, 1}, 10); got != 0 {
		t.Fatalf("a short list scrolls from 0, got %d", got)
	}
	if got := lineScrollMax(10, 8); got != 2 {
		t.Fatalf("line scroll = %d, want 2", got)
	}
}

func TestFollowCursor(t *testing.T) {
	if got := followCursor(0, 5, 4, 20); got != 0 {
		t.Fatalf("moving above the window = %d, want 0", got)
	}
	if got := followCursor(9, 0, 4, 20); got != 6 {
		t.Fatalf("moving below the window = %d, want 6", got)
	}
	if got := followCursor(3, 1, 4, 20); got != 1 {
		t.Fatalf("a cursor on screen moved the window to %d", got)
	}
}
