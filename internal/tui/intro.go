package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// First-run intro: a short, typed-in presentation of mymo, shown once on
// first launch (or replayed from settings). The brand is visible from the
// start; the rest reveals over ticks. Any key skips the animation.
const (
	introSteps = 2
	introTick  = 350 * time.Millisecond
)

// introTickMsg advances the intro reveal.
type introTickMsg struct{}

// nextIntroTick schedules the next reveal step.
func (m Model) nextIntroTick() tea.Cmd {
	return tea.Tick(introTick, func(time.Time) tea.Msg { return introTickMsg{} })
}

// updateIntro handles keys during the intro: the first press reveals the
// full text, the second enters the workspace (and remembers it happened).
func (m Model) updateIntro(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.introStep < introSteps {
		m.introStep = introSteps
		return m, nil
	}
	m.introDone = true
	if m.store != nil && !m.meta.IntroSeen {
		m.meta.IntroSeen = true
		if err := m.store.SaveMeta(m.meta); err != nil {
			m.introErr = err.Error()
		}
	}
	// The intro hands over to home, where the description starts typing.
	cmd := m.beginDesc()
	return m, cmd
}

// introView renders the intro, centered on the frame's canvas.
func (m Model) introView() string {
	lines := []string{brandIcon(), "", brandStyle.Render("mymo"), ""}
	if m.introStep >= 1 {
		lines = append(lines, accentStyle.Render("opinionated VPS operations"))
	}
	if m.introStep >= introSteps {
		lines = append(lines,
			dimStyle.Render("nodes · applications · deployments · health"),
			"",
			faintStyle.Render("press any key"))
	}
	if m.introErr != "" {
		lines = append(lines, "", errStyle.Render(m.introErr))
	}
	w := max(1, m.width-2)
	h := max(1, m.height-4)
	return lipgloss.Place(w, h,
		lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}
