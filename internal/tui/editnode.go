package tui

import (
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// startEditNode opens the edit form for the selected node. The name
// is not a field: renaming would orphan the applications and the
// key pair, which are stored under it. What changes is how mymo
// reaches the box.
func (m Model) startEditNode() (tea.Model, tea.Cmd) {
	n := m.selNode
	choice := acKey
	if n.Auth == domain.AuthAgent {
		choice = acAgent
	}
	vals := &addNodeValues{
		name:    n.Name,
		host:    n.Host,
		port:    strconv.Itoa(n.Port),
		user:    n.User,
		choice:  choice,
		keyPath: n.KeyPath,
	}
	m.addNode = addNodeState{stage: anForm, vals: vals, editing: true, node: n}
	v := vals
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Host or IP address").
				Value(&v.host).
				Validate(nonEmpty("host is required")),
			huh.NewInput().Title("SSH port").
				Value(&v.port).
				Validate(portInput),
			huh.NewInput().Title("SSH user").
				Value(&v.user).
				Validate(nonEmpty("user is required")),
			huh.NewSelect[authChoice]().Title("Authentication").
				Options(
					huh.NewOption("Key file", acKey),
					huh.NewOption("SSH agent", acAgent),
				).
				Value(&v.choice),
		),
		huh.NewGroup(
			huh.NewInput().Title("Private key path").
				Value(&v.keyPath).
				Validate(nonEmpty("key path is required")),
		).WithHideFunc(func() bool { return v.choice != acKey }),
	).WithTheme(HuhTheme()).WithShowHelp(true)
	m.addNode.form = form
	m.push(screen{kind: scEditNode, node: n.Name})
	m.layout()
	return m, form.Init()
}

// completeEditForm copies the form into a node that keeps everything
// the form does not ask about: name, mode, facts, bootstrap, and
// when the node was added.
func (m *Model) completeEditForm() {
	v := m.addNode.vals
	port, _ := strconv.Atoi(v.port)
	n := m.addNode.node
	n.Host = v.host
	n.Port = port
	n.User = v.user
	n.KeyPath = ""
	if v.choice == acKey {
		n.Auth = domain.AuthKey
		n.KeyPath = v.keyPath
	} else {
		n.Auth = domain.AuthAgent
	}
	m.addNode.node = n
	m.addNode.err = ""
	if err := n.Validate(); err != nil {
		m.addNode.err = err.Error()
	}
	m.addNode.stage = anReview
}

// updateEditReview saves the edited record or leaves it alone.
func (m Model) updateEditReview(str string) (tea.Model, tea.Cmd) {
	switch str {
	case "q":
		return m, tea.Quit
	case "c", "enter":
		if m.addNode.err != "" {
			return m, nil
		}
		return m.finishEdit()
	case "esc":
		m.pop()
		return m, nil
	}
	return m, nil
}

// finishEdit writes the connection fields and nothing else. A key
// path is checked before the write, the same way add does.
func (m Model) finishEdit() (tea.Model, tea.Cmd) {
	if m.store == nil {
		m.addNode.err = "state store unavailable"
		return m, nil
	}
	n := m.addNode.node
	if n.Auth == domain.AuthKey {
		if err := ssh.CheckKeyFile(n.KeyPath); err != nil {
			m.addNode.err = fmt.Sprintf("key file %q: %v", n.KeyPath, err)
			return m, nil
		}
	}
	cur, err := m.store.GetNode(n.Name)
	if err != nil {
		m.addNode.err = err.Error()
		return m, nil
	}
	// the form's copy was taken when the screen opened. fold the
	// connection fields onto the record as it is now, so a check
	// that landed during the edit is not thrown away.
	cur.Host = n.Host
	cur.Port = n.Port
	cur.User = n.User
	cur.Auth = n.Auth
	cur.KeyPath = n.KeyPath
	if err := m.store.UpdateNode(cur); err != nil {
		m.addNode.err = err.Error()
		return m, nil
	}
	name := cur.Name
	m.selNode = cur
	m.pop()
	m.reloadFleet()
	return m, m.notify(name+" updated — applications and the baseline record are unchanged", toastOK)
}

// editView renders the edit form or its review.
func (m Model) editView() string {
	if m.addNode.stage == anForm && m.addNode.form != nil {
		return m.addFormView()
	}
	n := m.addNode.node
	inner := titleStyle.Render("Edit "+n.Name) + "\n\n" +
		factsPanel(n, "host", "port", "user", "auth", "key path") +
		"\n\n" +
		faintStyle.Render("the name, the applications, and the baseline record stay.") + "\n" +
		faintStyle.Render("the box is not touched. the next check learns this address.") + "\n\n" +
		accentStyle.Render("[c]") + textStyle.Render(" save    ") +
		accentStyle.Render("[esc]") + textStyle.Render(" cancel")
	if m.addNode.err != "" {
		inner += "\n\n" + errStyle.Render(m.addNode.err)
	}
	return m.centeredPanel(inner)
}
