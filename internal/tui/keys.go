package tui

import (
	"charm.land/bubbles/v2/key"
)

// fleetKeys are the bindings shown on the fleet screen.
var fleetKeys = keyMap{
	quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	node: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "node details")),
}

// nodeKeys are the bindings shown on the node detail screen.
var nodeKeys = keyMap{
	quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
}

// keyMap is a help.KeyMap built from the enabled bindings it holds.
// Disabled (zero-value) bindings are omitted from the rendered help.
type keyMap struct {
	quit key.Binding
	node key.Binding
	back key.Binding
}

// ShortHelp satisfies help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	var out []key.Binding
	for _, b := range []key.Binding{k.node, k.back, k.quit} {
		if b.Enabled() {
			out = append(out, b)
		}
	}
	return out
}

// FullHelp satisfies help.KeyMap.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}
