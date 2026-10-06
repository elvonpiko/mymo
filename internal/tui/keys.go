package tui

import (
	"charm.land/bubbles/v2/key"
)

// Home keymap: the hub that leads everywhere.
type homeKeymap struct {
	nodes    key.Binding
	add      key.Binding
	apps     key.Binding
	deploy   key.Binding
	settings key.Binding
	help     key.Binding
	quit     key.Binding
}

func newHomeKeymap() homeKeymap {
	return homeKeymap{
		nodes:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "nodes")),
		add:      key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "add node")),
		apps:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apps")),
		deploy:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deploy")),
		settings: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "settings")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp stays minimal: the home page itself is the key guide.
func (k homeKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.help, k.quit}
}

func (k homeKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.nodes, k.add, k.apps, k.deploy, k.settings},
		{k.help, k.quit},
	}
}

// Fleet keymap: navigation, node selection, and workspace shortcuts.
type fleetKeymap struct {
	navigate key.Binding
	open     key.Binding
	add      key.Binding
	back     key.Binding
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
		add:      key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "add node")),
		back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "home")),
		filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		settings: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "settings")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		apps:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apps")),
		deploy:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deploy")),
	}
}

// ShortHelp keeps only the page's distinctive keys; arrows are
// universal list knowledge and the rest waits behind ?.
func (k fleetKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.open, k.add, k.filter, k.help, k.quit}
}

func (k fleetKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.navigate, k.open, k.filter, k.back},
		{k.add, k.apps, k.deploy, k.settings},
		{k.help, k.quit},
	}
}

// loadingKeymap applies while a probe runs on entry: only leaving and
// quitting are honest keys.
type loadingKeymap struct {
	back key.Binding
	help key.Binding
	quit key.Binding
}

func newLoadingKeymap() loadingKeymap {
	return loadingKeymap{
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k loadingKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.back, k.help, k.quit}
}

func (k loadingKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.back}, {k.help, k.quit}}
}

// Preflight keymap: a clear verdict continues to the plan, anything
// else leaves.
type preflightKeymap struct {
	plan key.Binding
	back key.Binding
	help key.Binding
	quit key.Binding
}

func newPreflightKeymap() preflightKeymap {
	return preflightKeymap{
		plan: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "plan")),
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k preflightKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.plan, k.back, k.help, k.quit}
}

func (k preflightKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.plan, k.back}, {k.help, k.quit}}
}

// Plan keymap: the plan changes nothing until its author applies it.
type planKeymap struct {
	apply key.Binding
	back  key.Binding
	help  key.Binding
	quit  key.Binding
}

func newPlanKeymap() planKeymap {
	return planKeymap{
		apply: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply")),
		back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:  key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k planKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.apply, k.back, k.help, k.quit}
}

func (k planKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.apply, k.back}, {k.help, k.quit}}
}

// Apply-confirm keymap: the node's name typed in full is the
// confirmation; esc is the only way out besides matching it.
type applyConfirmKeymap struct {
	confirm key.Binding
	back    key.Binding
	help    key.Binding
}

func newApplyConfirmKeymap() applyConfirmKeymap {
	return applyConfirmKeymap{
		confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

func (k applyConfirmKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.confirm, k.back, k.help}
}

func (k applyConfirmKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.confirm, k.back}, {k.help}}
}

// Apply-report keymap: the report stays until walked away from.
type applyReportKeymap struct {
	back key.Binding
	help key.Binding
	quit key.Binding
}

func newApplyReportKeymap() applyReportKeymap {
	return applyReportKeymap{
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k applyReportKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.back, k.help, k.quit}
}

func (k applyReportKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.back}, {k.help, k.quit}}
}

// Node keymap: the observe page's action list plus direct ssh and
// the app-host preflight.
type nodeKeymap struct {
	navigate key.Binding
	open     key.Binding
	ssh      key.Binding
	setup    key.Binding
	back     key.Binding
	help     key.Binding
	quit     key.Binding
}

func newNodeKeymap() nodeKeymap {
	return nodeKeymap{
		navigate: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "navigate")),
		open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		ssh:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "ssh")),
		setup:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "set up this node")),
		back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp keeps only the page's distinctive keys; everything else
// waits behind ?.
func (k nodeKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.open, k.ssh, k.setup, k.back, k.help, k.quit}
}

func (k nodeKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.navigate, k.open, k.ssh, k.setup},
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

// appsKeymap walks the applications pages: the record is the view,
// enter opens one application's history.
type appsKeymap struct {
	back  key.Binding
	help  key.Binding
	quit  key.Binding
	enter key.Binding
	up    key.Binding
	down  key.Binding
}

func newAppsKeymap() appsKeymap {
	return appsKeymap{
		back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:  key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	}
}

func (k appsKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.enter, k.up, k.down, k.back, k.help, k.quit}
}

func (k appsKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.up, k.down, k.enter}, {k.back, k.help, k.quit}}
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
