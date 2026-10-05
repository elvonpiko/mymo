package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// addNodeStage identifies the workflow's current step.
type addNodeStage int

const (
	anForm       addNodeStage = iota // embedded huh connection form
	anReview                         // review, then save (and first-contact plan)
	anInstalling                     // first contact running: key install + proof
)

// authChoice is how the operator says they connect today. Password
// is the fresh-box reality and the select's default; it maps to a
// key-auth node on file because mymo installs the key during first
// contact. The password itself never leaves this workflow's memory.
type authChoice string

const (
	acPassword authChoice = "password"
	acKey      authChoice = "key"
	acAgent    authChoice = "agent"
)

// addNodeValues holds the pointers the huh form binds to.
type addNodeValues struct {
	name     string
	host     string
	port     string
	user     string
	choice   authChoice
	password string
	keyPath  string
}

// addNodeState carries the workflow between its stages.
type addNodeState struct {
	stage addNodeStage
	form  *huh.Form
	// vals is a pointer shared with the form's bound accessors: the Model
	// struct is copied between updates, so a value field would fork from
	// the memory huh writes into.
	vals         *addNodeValues
	node         domain.Node
	firstContact bool
	err          string
}

// firstContactDoneMsg reports the onboarding's outcome; err is nil
// only when the key was proven on a second connection.
type firstContactDoneMsg struct {
	err error
}

// firstContactRun is the injectable first-contact runner; tests
// drive the workflow without a real node. The default runs the real
// transport: generate, install, prove.
var firstContactRun = func(host string, port int, user, password, keyPath, knownHostsPath string) error {
	return ssh.FirstContact(context.Background(), host, port, user, password, keyPath, knownHostsPath)
}

// startAddNode opens the add-node workflow on top of the navigation
// stack. The wizard mirrors what a provider hands you — host, port,
// user — asks for a name, and only then how you connect, with the
// fresh-box answer (password) first and its promise stated plainly.
func (m Model) startAddNode() (tea.Model, tea.Cmd) {
	vals := &addNodeValues{port: "22", choice: acPassword}
	m.addNode = addNodeState{stage: anForm, vals: vals}
	v := vals
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Host or IP address").
				Placeholder("203.0.113.10").
				Description("the address your provider emailed you").
				Value(&v.host).
				Validate(nonEmpty("host is required")),
			huh.NewInput().Title("SSH port").
				Value(&v.port).
				Validate(portInput),
			huh.NewInput().Title("SSH user").
				Placeholder("root").
				Description("the login user your provider emailed you").
				Value(&v.user).
				Validate(nonEmpty("user is required")),
			huh.NewInput().Title("Name").
				Placeholder("web-1").
				Description("one word mymo calls this node").
				Value(&v.name).
				Validate(domain.ValidateNodeName),
		),
		huh.NewGroup(
			huh.NewSelect[authChoice]().Title("How do you connect?").
				Description("password works for a fresh box: mymo installs a key, then forgets the password").
				Options(
					huh.NewOption("Password — first contact", acPassword),
					huh.NewOption("SSH key — I already have one", acKey),
					huh.NewOption("SSH agent", acAgent),
				).Value(&v.choice),
		),
		// the one conditional page: the password for first contact
		huh.NewGroup(
			huh.NewInput().Title("Password").
				Description("held in memory for one connection — never stored, never logged").
				Password(true).
				Value(&v.password).
				Validate(func(s string) error {
					if v.choice == acPassword && strings.TrimSpace(s) == "" {
						return errors.New("the password is required for first contact")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return v.choice != acPassword }),
		// or the key path for an existing key
		huh.NewGroup(
			huh.NewInput().Title("Private key path").
				Placeholder("/home/user/.ssh/id_ed25519").
				Value(&v.keyPath).
				Validate(func(s string) error {
					if v.choice == acKey && strings.TrimSpace(s) == "" {
						return errors.New("key path is required for key authentication")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return v.choice != acKey }),
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

// completeAddForm collects the form values into a node record and
// moves the workflow to the review stage. Password choice becomes a
// key-auth node whose key mymo generates on confirm — the review
// states that plan before anything runs.
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
		Mode:    domain.ModeObserve,
		AddedAt: time.Now(),
	}
	switch m.addNode.vals.choice {
	case acPassword:
		// the node on file is key-auth; first contact installs the
		// dedicated key before anything is saved
		n.Auth = domain.AuthKey
		if m.store != nil {
			n.KeyPath = ssh.FirstContactKeyPath(m.store.Dir(), n.Name)
		}
		m.addNode.firstContact = true
	case acKey:
		n.Auth = domain.AuthKey
		n.KeyPath = strings.TrimSpace(m.addNode.vals.keyPath)
	default:
		n.Auth = domain.AuthAgent
	}
	m.addNode.node = n
	m.addNode.err = ""
	if err := n.Validate(); err != nil {
		m.addNode.err = err.Error()
	}
	m.addNode.stage = anReview
}

// updateAddReview handles keys on the review step: confirm or cancel.
// Confirm runs first contact for a password choice — the node is
// saved only when the key was proven on a second connection.
func (m Model) updateAddReview(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "c", "enter":
		if m.addNode.firstContact {
			if m.store == nil {
				m.addNode.err = "state store unavailable"
				return m, nil
			}
			node := m.addNode.node
			password := m.addNode.vals.password
			keyPath := node.KeyPath
			m.addNode.stage = anInstalling
			m.loading = loadingState{active: true, node: node.Name,
				line:   "first contact with " + node.Host,
				detail: "installing the dedicated key, proving it works",
				start:  time.Now()}
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				err := firstContactRun(node.Host, node.Port, node.User, password, keyPath, knownHostsPathTUI(m.store))
				return firstContactDoneMsg{err: err}
			})
		}
		return m.finishAdd()
	case "esc":
		m.pop()
		return m, nil
	}
	return m, nil
}

// handleFirstContactDone settles the onboarding: on success the node
// is saved and the toast says what changed; on failure nothing is
// saved and the review screen says exactly what went wrong.
func (m Model) handleFirstContactDone(msg firstContactDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = loadingState{}
	m.addNode.stage = anReview
	if msg.err != nil {
		if errors.Is(msg.err, ssh.ErrPasswordExpired) {
			// the forced reset is the box's condition, not a failure
			// report: the move is one line and stays the operator's
			m.addNode.err = "password change forced: ssh in once, change it, then run first contact again"
		} else {
			m.addNode.err = msg.err.Error()
		}
		return m, nil
	}
	// the password served its one connection; the values the form
	// held go out of scope with this copy's workflow state
	m.addNode.vals.password = ""
	return m.finishAdd()
}

// finishAdd saves the reviewed node and lands back on the fleet.
func (m Model) finishAdd() (tea.Model, tea.Cmd) {
	node := m.addNode.node
	// pop() below wipes the workflow state when the screen leaves the
	// stack — the toast's wording depends on knowing how this node
	// came in, so the fact is read first
	firstContact := m.addNode.firstContact
	if err := m.saveNewNode(node); err != nil {
		m.addNode.err = err.Error()
		return m, nil
	}
	m.pop()
	m.reloadFleet()
	if firstContact {
		return m, m.notify(node.Name+" is key-auth now — the password was never stored", toastOK)
	}
	return m, m.notify("added "+node.Name+" to the fleet", toastOK)
}

// updateInstalling handles keys while first contact runs: esc
// cancels back to the review, anything else waits.
func (m Model) updateInstalling(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "esc":
		m.loading = loadingState{}
		m.addNode.stage = anReview
		m.addNode.err = "first contact cancelled — nothing was changed on the node"
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
	switch {
	case m.addNode.stage == anForm:
		return m.addFormView()
	case m.addNode.stage == anInstalling:
		return m.loadingView()
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

// addReviewView renders the review step: for first contact, the
// exact plan that will run; otherwise the record to be saved.
func (m Model) addReviewView() string {
	n := m.addNode.node
	inner := titleStyle.Render("Review new node") + "\n\n"
	if m.addNode.firstContact {
		// one identity line, not a fact table: the floor reserves 19
		// rows for this page, and the failure note — which can wrap —
		// must stay visible at the bottom
		inner += textStyle.Render(n.Name) + faintStyle.Render(" · ") +
			textStyle.Render(n.Host+":"+fmt.Sprint(n.Port)) + faintStyle.Render(" · ") +
			textStyle.Render(n.User) + "\n"
		inner += faintStyle.Render("first contact — in this order:") + "\n" +
			"  1. generate a dedicated key beside your local state\n" +
			"  2. connect once with the password\n" +
			"  3. append it to authorized_keys — append-only, never rewriting it\n" +
			"  4. prove the key works on a second connection\n" +
			"  5. save " + n.Name + " as a key-auth node\n" +
			faintStyle.Render("the password is never stored, never logged") + "\n\n" +
			accentStyle.Render("[c]") + textStyle.Render(" run first contact    ") +
			accentStyle.Render("[esc]") + textStyle.Render(" cancel")
	} else {
		inner += factsPanel(n, "name", "host", "port", "user", "auth", "key path") +
			"\n\n" +
			faintStyle.Render("Nodes start in observe mode. mymo never modifies a server") + "\n" +
			faintStyle.Render("without your typed approval — and password authentication") + "\n" +
			faintStyle.Render("is only ever a first contact, never a way of life.") + "\n\n" +
			accentStyle.Render("[c]") + textStyle.Render(" save to fleet    ") +
			accentStyle.Render("[esc]") + textStyle.Render(" cancel")
	}
	if m.addNode.err != "" {
		inner += "\n\n"
		for _, l := range wrapDetail(m.addNode.err, m.stageW-8) {
			inner += errStyle.Render(l) + "\n"
		}
	}
	return lipgloss.Place(m.contentWidth, m.contentHeight,
		lipgloss.Center, lipgloss.Center, cardStyle.Render(inner))
}

// knownHostsPathTUI is the first-contact trust store: the same TOFU
// file the rest of the transport reads.
func knownHostsPathTUI(store *state.Store) string {
	return filepath.Join(store.Dir(), "known_hosts.json")
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
