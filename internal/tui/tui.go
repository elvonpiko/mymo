// Package tui implements mymo's terminal user interface. It is a pure
// presentation layer over the domain and state packages.
package tui

import (
	"errors"
	"os"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
)

// ErrNoTerminal is returned when the TUI is started without a terminal.
var ErrNoTerminal = errors.New("mymo requires an interactive terminal")

type viewID int

const (
	viewFleet viewID = iota
	viewNode
)

// Model is the mymo TUI root model.
type Model struct {
	store   *state.Store
	nodes   []domain.Node
	fleet   list.Model
	help    help.Model
	view    viewID
	sel     domain.Node // node shown in the detail view
	width   int
	height  int
	loadErr error
}

// New builds the TUI model from the given store. State loading failures are
// surfaced inside the UI, not here.
func New(store *state.Store) Model {
	var (
		nodes   []domain.Node
		loadErr error
	)
	if store != nil {
		nodes, loadErr = store.LoadNodes()
	}
	m := Model{
		store:   store,
		nodes:   nodes,
		fleet:   newFleetList(nodes),
		help:    help.New(),
		width:   80,
		height:  24,
		loadErr: loadErr,
	}
	m.layout()
	return m
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update satisfies tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyPressMsg:
		// While the user is typing a filter, keys belong to the filter
		// input; only ctrl+c keeps its global meaning.
		if m.fleet.FilterState() == list.Filtering && msg.String() != "ctrl+c" {
			break
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.view == viewNode {
				m.view = viewFleet
				return m, nil
			}
		case "enter":
			if m.view == viewFleet {
				if item, ok := m.fleet.SelectedItem().(fleetItem); ok {
					m.sel = item.node
					m.view = viewNode
					return m, nil
				}
			}
		}
	}
	if m.view == viewFleet {
		var cmd tea.Cmd
		m.fleet, cmd = m.fleet.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View satisfies tea.Model.
func (m Model) View() tea.View {
	var content string
	switch m.view {
	case viewNode:
		content = m.nodeView()
	default:
		content = m.fleetView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// layout sizes children to fit the current window: one header row, one
// blank row, and one footer row of chrome.
func (m *Model) layout() {
	h := m.height - 3
	if h < 1 {
		h = 1
	}
	m.fleet.SetSize(m.width, h)
}

// Run starts the interactive TUI against the given store. It returns
// ErrNoTerminal when stdin or stdout is not a terminal.
func Run(store *state.Store) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return ErrNoTerminal
	}
	_, err := tea.NewProgram(New(store)).Run()
	return err
}
