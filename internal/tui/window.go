package tui

// scrollMaxHeights is the first row a window may start on and still
// show its last row in full. Rows are measured by height and the cut
// is always between them. A last row taller than the window is still
// a legal start: the page can open on it.
func scrollMaxHeights(heights []int, rows int) int {
	if len(heights) == 0 {
		return 0
	}
	if rows < 1 {
		rows = 1
	}
	taken, last := 0, len(heights)-1
	for i := len(heights) - 1; i >= 0; i-- {
		h := heights[i]
		if h < 1 {
			h = 1
		}
		if taken+h > rows {
			break
		}
		taken += h
		last = i
	}
	if last < 0 {
		return 0
	}
	return last
}

// lineScrollMax is scrollMaxHeights for a list of single-line rows.
func lineScrollMax(n, rows int) int {
	if n <= 0 {
		return 0
	}
	heights := make([]int, n)
	for i := range heights {
		heights[i] = 1
	}
	return scrollMaxHeights(heights, rows)
}

// followCursor keeps a selected row inside the window. Moving up
// pulls the window up; moving past the bottom pulls it down. A
// cursor already on screen leaves the window where it is.
func followCursor(cursor, scroll, window, n int) int {
	if n <= 0 {
		return 0
	}
	if window < 1 {
		window = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	if cursor < scroll {
		return cursor
	}
	if cursor >= scroll+window {
		return cursor - window + 1
	}
	return scroll
}
