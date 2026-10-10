package apply

import "github.com/elvonpiko/mymo/internal/preflight"

// Row is one control's honest position on the finished page: what
// the live probes said where they exist (OK, or FAILED with the
// reason), what merely landed because no live probe covers it
// (DONE), and what was never attempted or never ran to completion
// (BLOCKED, FAILED). One row per control — never a second list
// under the first telling a different story.
type Row struct {
	Control string
	Title   string
	Status  string // OK, DONE, FAILED, BLOCKED
	Detail  []string
}

// verifyControl maps each live probe to the step it proves: the
// finished page speaks in the plan's step names, and the probe's
// own title never appears twice in two different lists.
var verifyControl = map[string]string{
	"sshd posture":    "ssh-hardening",
	"firewall":        "firewall",
	"docker":          "docker-daemon",
	"mymo user":       "mymo-user",
	"mymo key access": "mymo-user",
	"baseline marker": "state-dir",
	"caddy":           "caddy",
	"auto-updates":    "unattended-upgrades",
}

// BuildReport merges the apply's step outcomes with the live
// verification probes into one row per control. The live truth wins
// over the apply's bookkeeping: a step that ran fine but does not
// hold in effective state reads FAILED with the probe's reason, not
// DONE — the box's word for itself outranks mymo's claim about it.
func BuildReport(res Result, checks []preflight.Check) []Row {
	live := map[string][]int{}
	for i, c := range checks {
		if ctl, ok := verifyControl[c.Title]; ok {
			live[ctl] = append(live[ctl], i)
		}
	}
	used := make([]bool, len(checks))
	rows := make([]Row, 0, len(res.Steps))
	for _, s := range res.Steps {
		r := Row{Control: s.Control, Title: s.Title, Status: "DONE", Detail: []string{s.Detail}}
		switch s.State {
		case StepFailed:
			// the apply stopped here: the note is the reason, the
			// only honest detail a stopped step can carry
			r.Status, r.Detail = "FAILED", []string{s.Note}
		case StepBlocked:
			r.Status, r.Detail = "BLOCKED", nil
		default:
			var held []string
			for _, i := range live[s.Control] {
				used[i] = true
				c := checks[i]
				if c.Outcome == preflight.Pass {
					held = append(held, c.Detail)
				} else {
					r.Status = "FAILED"
					r.Detail = append(r.Detail, c.Detail)
				}
			}
			if r.Status != "FAILED" && len(held) > 0 {
				r.Status = "OK"
				r.Detail = held
			}
		}
		rows = append(rows, r)
	}
	// a probe no step consumed is never dropped: it gets its own
	// row, because an unnamed truth is a hidden one — this also
	// keeps a probe from a newer mymo visible against an older plan
	for i, c := range checks {
		if !used[i] {
			status := "OK"
			if c.Outcome != preflight.Pass {
				status = "FAILED"
			}
			rows = append(rows, Row{Control: c.Title, Title: c.Title, Status: status, Detail: []string{c.Detail}})
		}
	}
	return rows
}
