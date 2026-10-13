package tui

import (
	"charm.land/bubbles/v2/help"
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

// homeHint is the one global shortcut: h is home from anywhere, so
// the way back is never a guess.
var homeHint = key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "home"))

// keymapWithHome appends the home shortcut to any page's keymap: one
// adapter, every footer.
type keymapWithHome struct{ inner help.KeyMap }

func (k keymapWithHome) ShortHelp() []key.Binding {
	return append(k.inner.ShortHelp(), homeHint)
}

func (k keymapWithHome) FullHelp() [][]key.Binding {
	groups := k.inner.FullHelp()
	if len(groups) == 0 {
		return [][]key.Binding{{homeHint}}
	}
	groups[len(groups)-1] = append(groups[len(groups)-1], homeHint)
	return groups
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
	remove   key.Binding
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
		remove:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
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
	return []key.Binding{k.open, k.add, k.remove, k.apps, k.help, k.quit}
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

// Preflight keymap: a clear verdict continues to the plan, a
// decision can be taken, anything else leaves.
type preflightKeymap struct {
	plan   key.Binding
	decide key.Binding
	back   key.Binding
	help   key.Binding
	quit   key.Binding
}

func newPreflightKeymap() preflightKeymap {
	return preflightKeymap{
		plan:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "plan")),
		decide: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "decide + plan")),
		back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:   key.NewBinding(key.WithKeys("?", "help"), key.WithHelp("?", "help")),
		quit:   key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k preflightKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.plan, k.decide, k.back, k.help, k.quit}
}

func (k preflightKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.plan, k.decide, k.back}, {k.help, k.quit}}
}

// Plan keymap: the plan changes nothing until its author applies it.
// Plan keymap: the page is one input — every printable rune types
// into the name, so the footer advertises only what leaves. The
// page's own hint line ("type <name> in full to apply — anything
// else aborts") carries the affordance.
type planKeymap struct {
	back key.Binding
}

func newPlanKeymap() planKeymap {
	return planKeymap{
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}

func (k planKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.back}
}

func (k planKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.back}}
}

// Apply-report keymap: the report stays until walked away from.
type applyReportKeymap struct {
	open key.Binding
	help key.Binding
	quit key.Binding
}

func newApplyReportKeymap() applyReportKeymap {
	return applyReportKeymap{
		open: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "node")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k applyReportKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.open, k.help, k.quit}
}

func (k applyReportKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.open}, {k.help, k.quit}}
}

// Node keymap: the observe page's action list plus direct ssh and
// the app-host preflight.
type nodeKeymap struct {
	navigate key.Binding
	ssh      key.Binding
	setup    key.Binding
	apps     key.Binding
	inspect  key.Binding
	remove   key.Binding
	back     key.Binding
	help     key.Binding
	quit     key.Binding
}

func newNodeKeymap() nodeKeymap {
	return nodeKeymap{
		navigate: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "navigate")),
		ssh:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "ssh")),
		setup:    key.NewBinding(key.WithKeys("I"), key.WithHelp("I", "setup")),
		apps:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apps")),
		inspect:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "record")),
		remove:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
		back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "home")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp keeps only the page's distinctive keys; everything else
// waits behind ?.
func (k nodeKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.setup, k.ssh, k.apps, k.inspect, k.remove, k.help, k.quit}
}

func (k nodeKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.navigate, k.ssh, k.setup, k.apps, k.inspect, k.remove},
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

// appDetailKeymap: the verbs are keys, one letter each — the
// record above folds honestly when long, and the rows the menu
// spent belong to it again.
type appDetailKeymap struct {
	status   key.Binding
	logs     key.Binding
	restart  key.Binding
	rollback key.Binding
	shell    key.Binding
	back     key.Binding
	help     key.Binding
	quit     key.Binding
}

func newAppDetailKeymap() appDetailKeymap {
	return appDetailKeymap{
		status:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "status")),
		logs:     key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
		restart:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		rollback: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "rollback")),
		shell:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "shell")),
		back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k appDetailKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.status, k.logs, k.restart, k.rollback, k.shell, k.help, k.quit}
}

func (k appDetailKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.status, k.logs, k.restart, k.rollback, k.shell},
		{k.back, k.help, k.quit},
	}
}

// appLogsKeymap: one scrollable target — the tail — and the way out.
type appLogsKeymap struct {
	scrollUp   key.Binding
	scrollDown key.Binding
	back       key.Binding
	help       key.Binding
	quit       key.Binding
}

func newAppLogsKeymap() appLogsKeymap {
	return appLogsKeymap{
		scrollUp:   key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "scroll up")),
		scrollDown: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "scroll down")),
		back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:       key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

func (k appLogsKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.scrollUp, k.scrollDown, k.back, k.help, k.quit}
}

func (k appLogsKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.scrollUp, k.scrollDown}, {k.back, k.help, k.quit}}
}

// appRollbackKeymap: the input owns the keyboard — the name typed
// in full is the confirmation.
type appRollbackKeymap struct {
	back key.Binding
}

func newAppRollbackKeymap() appRollbackKeymap {
	return appRollbackKeymap{
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "abort")),
	}
}

func (k appRollbackKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.back}
}

func (k appRollbackKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.back}}
}

// deployFlowKeymap: the walker walks — the pick stage's j/k match —
// enter opens, u climbs; the confirm card states its own ceremony.
type deployFlowKeymap struct {
	up    key.Binding
	down  key.Binding
	open  key.Binding
	climb key.Binding
	back  key.Binding
	help  key.Binding
	quit  key.Binding
}

func newDeployFlowKeymap() deployFlowKeymap {
	return deployFlowKeymap{
		up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		open:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		climb: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "up")),
		back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:  key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp keeps only the page's distinctive keys — arrows are
// universal list knowledge, like the fleet page.
func (k deployFlowKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.open, k.climb, k.back, k.help, k.quit}
}

func (k deployFlowKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.up, k.down, k.open, k.climb}, {k.back}}
}

// deployReportKeymap: the finished report exits forward.
type deployReportKeymap struct {
	open key.Binding
}

func newDeployReportKeymap() deployReportKeymap {
	return deployReportKeymap{
		open: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "app")),
	}
}

func (k deployReportKeymap) ShortHelp() []key.Binding {
	return []key.Binding{k.open}
}

func (k deployReportKeymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.open}}
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
