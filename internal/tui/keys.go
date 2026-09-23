package tui

import (
	"charm.land/bubbles/v2/key"
)

// Fleet keymap: navigation, node selection, and workspace shortcuts.
type fleetKeymap struct {
	navigate key.Binding
	open     key.Binding
	add      key.Binding
	filter   key.Binding
	settings key.Binding
	help     key.Binding
	quit     key.Binding
	apps     key.Binding
	deploy   key.Binding
}

func newFleetKeymap() fleetKeymap {
	return fleetKeymap{
		navigate: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "navigate")),
		open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		add:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "add node")),
		filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		settings: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "settings")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		apps:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apps")),
		deploy:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deploy")),
	}
}

func (k fleetKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.navigate, k.open, k.add, k.filter, k.help, k.quit}
}

func (k fleetKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.navigate, k.open, k.filter},
		{k.add, k.apps, k.deploy, k.settings},
		{k.help, k.quit},
	}
}

// Node keymap: the node's action list.
type nodeKeymap struct {
	navigate key.Binding
	open     key.Binding
	back     key.Binding
	help     key.Binding
	quit     key.Binding
}

func newNodeKeymap() nodeKeymap {
	return nodeKeymap{
		navigate: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "navigate")),
		open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k nodeKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.navigate, k.open, k.back, k.help, k.quit}
}

func (k nodeKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.navigate, k.open},
		{k.back},
		{k.help, k.quit},
	}
}

// simpleKeymap applies to informational screens without interactive widgets.
type simpleKeymap struct {
	back key.Binding
	help key.Binding
	quit key.Binding
}

func newSimpleKeymap() simpleKeymap {
	return simpleKeymap{
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k simpleKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.back, k.help, k.quit}
}

func (k simpleKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.back}, {k.help, k.quit}}
}

// addReviewKeymap applies to the review step of the add-node workflow.
type addReviewKeymap struct {
	confirm key.Binding
	cancel  key.Binding
	help    key.Binding
	quit    key.Binding
}

func newAddReviewKeymap() addReviewKeymap {
	return addReviewKeymap{
		confirm: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "save")),
		cancel:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k addReviewKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.confirm, k.cancel, k.help, k.quit}
}

func (k addReviewKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.confirm, k.cancel}, {k.help, k.quit}}
}

// formKeymap applies while an embedded form owns every key; only the
// abort key is honest to show, since typing goes to the fields.
type formKeymap struct {
	cancel key.Binding
}

func newFormKeymap() formKeymap {
	return formKeymap{
		cancel: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel")),
	}
}

func (k formKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.cancel}
}

func (k formKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.cancel}}
}

// confirmKeymap applies to destructive-action confirmations.
type confirmKeymap struct {
	confirm key.Binding
	cancel  key.Binding
	quit    key.Binding
}

func newConfirmKeymap() confirmKeymap {
	return confirmKeymap{
		confirm: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
		cancel:  key.NewBinding(key.WithKeys("esc", "n"), key.WithHelp("esc", "cancel")),
		quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k confirmKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.confirm, k.cancel, k.quit}
}

func (k confirmKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.confirm, k.cancel}, {k.quit}}
}
