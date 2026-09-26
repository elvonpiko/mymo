package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// addNodeStage identifies the workflow's current step.
type addNodeStage int

const (
	anForm   addNodeStage = iota // embedded huh connection form
	anReview                     // review, then save
)

// addNodeValues holds the pointers the huh form binds to.
type addNodeValues struct {
	name    string
	host    string
	port    string
	user    string
	auth    domain.AuthMethod
	keyPath string
}

// addNodeState carries the workflow between its stages.
type addNodeState struct {
	stage addNodeStage
	form  *huh.Form
	// vals is a pointer shared with the form's bound accessors: the Model
	// struct is copied between updates, so a value field would fork from
	// the memory huh writes into.
	vals *addNodeValues
	node domain.Node
	err  string
}

// startAddNode opens the add-node workflow on top of the navigation stack.
func (m Model) startAddNode() (tea.Model, tea.Cmd) {
	vals := &addNodeValues{port: "22", auth: domain.AuthKey}
	m.addNode = addNodeState{stage: anForm, vals: vals}
	v := vals
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Name").
				Placeholder("web-1").
				Value(&v.name).
				Validate(domain.ValidateNodeName),
			huh.NewInput().Title("Host or IP address").
				Placeholder("203.0.113.10").
				Value(&v.host).
				Validate(nonEmpty("host is required")),
			huh.NewInput().Title("SSH port").
				Value(&v.port).
				Validate(portInput),
			huh.NewInput().Title("SSH user").
				Placeholder("root").
				Value(&v.user).
				Validate(nonEmpty("user is required")),
			huh.NewSelect[domain.AuthMethod]().Title("Authentication").
				Options(
					huh.NewOption("SSH key", domain.AuthKey),
					huh.NewOption("SSH agent", domain.AuthAgent),
				).Value(&v.auth),
			huh.NewInput().Title("Private key path").
				Placeholder("/home/user/.ssh/id_ed25519").
				Value(&v.keyPath).
				Validate(func(s string) error {
					if v.auth == domain.AuthKey && strings.TrimSpace(s) == "" {
						return errors.New("key path is required for key authentication")
					}
					return nil
				}),
		),
	).WithShowHelp(true).WithTheme(HuhTheme())
	m.addNode.form = form
	m.push(screen{kind: scAddNode})
	m.layout()
	return m, form.Init()
}

// updateAddForm forwards messages to the embedded form and transitions
// between the workflow's stages.
func (m Model) updateAddForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.addNode.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		m.addNode.form = f
	}
	switch m.addNode.form.State {
	case huh.StateCompleted:
		if m.addNode.stage == anForm {
			m.completeAddForm()
		}
	case huh.StateAborted:
		m.pop()
		return m, nil
	}
	return m, cmd
}

// completeAddForm collects the form values into a node record and moves
// the workflow to the review stage.
func (m *Model) completeAddForm() {
	port, err := strconv.Atoi(strings.TrimSpace(m.addNode.vals.port))
	if err != nil {
		m.addNode.err = fmt.Sprintf("invalid port %q", m.addNode.vals.port)
		m.addNode.stage = anReview
		return
	}
	n := domain.Node{
		Name:    strings.TrimSpace(m.addNode.vals.name),
		Host:    strings.TrimSpace(m.addNode.vals.host),
		Port:    port,
		User:    strings.TrimSpace(m.addNode.vals.user),
		Auth:    m.addNode.vals.auth,
		KeyPath: strings.TrimSpace(m.addNode.vals.keyPath),
		Mode:    domain.ModeObserve,
		AddedAt: time.Now(),
	}
	m.addNode.node = n
	m.addNode.err = ""
	if err := n.Validate(); err != nil {
		m.addNode.err = err.Error()
	}
	m.addNode.stage = anReview
}

// updateAddReview handles keys on the review step: save or cancel.
func (m Model) updateAddReview(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "c", "enter":
		node := m.addNode.node
		if err := m.saveNewNode(node); err != nil {
			m.addNode.err = err.Error()
			return m, nil
		}
		m.pop()
		m.reloadFleet()
		return m, m.notify("added "+node.Name+" to the fleet", toastOK)
	case "esc":
		m.pop()
		return m, nil
	}
	return m, nil
}

// saveNewNode validates the key file (when needed) and persists the node.
func (m Model) saveNewNode(n domain.Node) error {
	if m.store == nil {
		return errors.New("state store unavailable")
	}
	if n.Auth == domain.AuthKey {
		if err := ssh.CheckKeyFile(n.KeyPath); err != nil {
			return fmt.Errorf("key file %q: %v", n.KeyPath, err)
		}
	}
	return m.store.AddNode(n)
}

// addView renders the add-node workflow's current stage.
func (m Model) addView() string {
	if m.addNode.stage == anForm {
		return m.addFormView()
	}
	return m.addReviewView()
}

// addFormView renders the embedded huh form as an interactive card,
// centered in the canvas both horizontally and vertically.
func (m Model) addFormView() string {
	card := cardStyle.Render(m.addNode.form.View())
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, card)
}

// addReviewView renders the review step: the record to be saved, the
// confirm and cancel keys, and any error from saving.
func (m Model) addReviewView() string {
	n := m.addNode.node
	inner := titleStyle.Render("Review new node") + "\n\n" +
		factsPanel(n, "name", "host", "port", "user", "auth", "key path") +
		"\n\n" +
		faintStyle.Render("Nodes start in observe mode. SSH discovery and") + "\n" +
		faintStyle.Render("app-host preparation arrive with the transport phase;") + "\n" +
		faintStyle.Render("mymo never modifies a server without your approval.") + "\n\n" +
		accentStyle.Render("[c]") + textStyle.Render(" save to fleet    ") +
		accentStyle.Render("[esc]") + textStyle.Render(" cancel")
	if m.addNode.err != "" {
		inner += "\n\n" + errStyle.Render(m.addNode.err)
	}
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, cardStyle.Render(inner))
}

// nonEmpty and portInput are the small validators shared by the form.
func nonEmpty(msg string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(msg)
		}
		return nil
	}
}

func portInput(s string) error {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return errors.New("port must be a number between 1 and 65535")
	}
	return nil
}
