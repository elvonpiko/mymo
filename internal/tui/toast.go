package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// toastTTL is how long a notification stays visible.
const toastTTL = 4 * time.Second

type toastKind int

const (
	toastOK toastKind = iota
	toastWarn
	toastErr
)

// toast is a transient, non-blocking notification shown in the header.
type toast struct {
	id   int
	text string
	kind toastKind
}

// toastExpiredMsg dismisses the toast with the given id.
type toastExpiredMsg struct{ id int }

var toastSeq int

// notify shows a toast and schedules its dismissal. It never blocks the UI.
func (m *Model) notify(text string, kind toastKind) tea.Cmd {
	toastSeq++
	m.toast = &toast{id: toastSeq, text: text, kind: kind}
	id := toastSeq
	return tea.Tick(toastTTL, func(time.Time) tea.Msg { return toastExpiredMsg{id: id} })
}

func (t toast) view() string {
	switch t.kind {
	case toastOK:
		return okStyle.Render("✓ ") + textStyle.Render(t.text)
	case toastWarn:
		return warnStyle.Render("! ") + textStyle.Render(t.text)
	default:
		return errStyle.Render("✗ ") + textStyle.Render(t.text)
	}
}
