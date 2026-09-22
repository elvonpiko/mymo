// Package tui implements mymo's terminal user interface: one persistent,
// full-window workspace with a navigation stack over the fleet, nodes,
// and their applications. It is a pure presentation layer over the domain
// and state packages.
package tui

import (
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/elvonpiko/mymo/internal/state"
)

// ErrNoTerminal is returned when the TUI is started without a terminal.
var ErrNoTerminal = errors.New("mymo requires an interactive terminal")

// Run starts the interactive workspace TUI against the given store. It
// returns ErrNoTerminal when stdin or stdout is not a terminal.
func Run(store *state.Store) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return ErrNoTerminal
	}
	_, err := tea.NewProgram(New(store)).Run()
	return err
}
