// Package apply executes a confirmed plan on the node. It is the only
// code in mymo that changes a server, and it is shaped by the
// lifecycle's rules:
//
//   - steps run in the plan's order and stop at the first failure;
//     nothing after a failure is attempted
//   - files already holding the intended content are skipped;
//     files that differ are backed up beside themselves (.mymo-bak,
//     preserving the original across re-runs) before being replaced
//   - writes land as a temporary file that is chmod-ed and chown-ed
//     before an atomic move into place — a half-written file never
//     becomes the live one
//   - the sshd step's gate is not optional: the reload — the only
//     command that can disable the operator's old way in — runs only
//     after the new mymo key is proven on a second connection, with
//     the operator's session still open
//   - every command runs through sudo -n when the operator is not
//     root, so lost privilege fails loudly instead of half-applying
//   - apply never deletes anything and never resets what it did not
//     configure; it reports exactly what it did, kept, and refused
package apply

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/plan"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// pubKeyPlaceholder marks where the generated mymo key's public half
// lands; the private half never leaves the local machine.
const pubKeyPlaceholder = "<mymo-public-key>"

// appliedAtPlaceholder marks where the apply's timestamp lands in the
// baseline marker the state-dir step writes.
const appliedAtPlaceholder = "<at apply>"

// Prover proves the new mymo key the way it will be used after the
// reload: a real second connection as the mymo user. Apply refuses a
// gated step without one.
type Prover interface {
	ProveMymoKey(ctx context.Context) error
}

// StepState is one step's outcome.
type StepState string

const (
	// StepDone means every command ran.
	StepDone StepState = "done"
	// StepKept means the node already had the intended state; the
	// step validated it instead of changing it.
	StepKept StepState = "kept"
	// StepFailed means a command failed; the apply stopped here.
	StepFailed StepState = "failed"
	// StepBlocked means an earlier failure prevented this step from
	// ever running.
	StepBlocked StepState = "blocked"
)

// StepResult reports one step's outcome for the review.
type StepResult struct {
	Control string
	Title   string
	State   StepState
	Note    string
}

// Result is the apply's report: what ran, what was kept, and where it
// stopped if it stopped early.
type Result struct {
	Steps      []StepResult
	Failed     bool
	FailedAt   string // control of the failed step, empty on success
	GateProven bool   // the second-connection proof happened
}

// Options carry what the caller owns: how to run privileged commands,
// the generated mymo key, the gate's prover, and progress reporting.
type Options struct {
	// Sudo prefixes every remote command with sudo -n — for operators
	// who connect as a regular user with passwordless sudo.
	Sudo bool
	// MymoPublicKey is the generated key's public half, installed for
	// the mymo user. Required.
	MymoPublicKey string
	// Now stamps the baseline marker's appliedAt field.
	Now time.Time
	// Prover proves the new key on a second connection before the
	// sshd reload. Required for gated steps.
	Prover Prover
	// Progress receives one line per notable event; may be nil.
	Progress func(line string)
}

// Apply runs the plan's steps in order. It never retries and never
// guesses: the first failure ends the run, and the result says
// exactly where.
func Apply(ctx context.Context, r preflight.Runner, steps []plan.Step, o Options) Result {
	res := Result{Steps: make([]StepResult, 0, len(steps))}

	fail := func(i int, control, title, note string) Result {
		res.Steps = append(res.Steps, StepResult{Control: control, Title: title, State: StepFailed, Note: note})
		for _, rest := range steps[i+1:] {
			res.Steps = append(res.Steps, StepResult{Control: rest.Control, Title: rest.Title, State: StepBlocked})
		}
		res.Failed = true
		res.FailedAt = control
		return res
	}

	for i, s := range steps {
		o.report(fmt.Sprintf("step %d/%d · %s", i+1, len(steps), s.Title))

		if s.Gate != "" && o.Prover == nil {
			return fail(i, s.Control, s.Title, "the sshd gate requires a second-connection prover; none was provided")
		}

		// files first, exactly as the plan stated them
		// files first, exactly as the plan stated them
		kept := true
		for _, f := range s.Files {
			fileKept, err := writeFile(ctx, r, f, o)
			kept = kept && fileKept
			if err != nil {
				return fail(i, s.Control, s.Title, err.Error())
			}
		}

		// a gated step runs everything but its last command, proves
		// the new key on a second connection, and only then commits:
		// the last command is the one that can close the old door
		exec := s.Exec
		if s.Gate != "" {
			if len(exec) < 2 {
				return fail(i, s.Control, s.Title, "a gated step must end with its commit command")
			}
			preKept, err := runAll(ctx, r, exec[:len(exec)-1], o)
			kept = kept && preKept
			if err != nil {
				return fail(i, s.Control, s.Title, err.Error())
			}
			o.report("proving the mymo key on a second connection — your current access is untouched")
			if err := o.Prover.ProveMymoKey(ctx); err != nil {
				return fail(i, s.Control, s.Title,
					"the gate refused — "+err.Error()+"; sshd was not reloaded and the operator's current access is untouched")
			}
			res.GateProven = true
			o.report("the new key is proven — sshd reloads next")
			exec = exec[len(exec)-1:]
		}

		execKept, err := runAll(ctx, r, exec, o)
		kept = kept && execKept
		if err != nil {
			return fail(i, s.Control, s.Title, err.Error())
		}
		state := StepDone
		if kept && (len(s.Files) > 0 || len(s.Exec) > 0) {
			state = StepKept
		}
		res.Steps = append(res.Steps, StepResult{Control: s.Control, Title: s.Title, State: state})
	}
	return res
}

// runArgv splits one wrapped command into the Runner's (name, args)
// shape.
func runArgv(ctx context.Context, r preflight.Runner, argv []string) (string, int, error) {
	if len(argv) == 0 {
		return "", 0, fmt.Errorf("empty command")
	}
	return r.Run(ctx, argv[0], argv[1:]...)
}

// report forwards a progress line when a reporter is attached.
func (o Options) report(line string) {
	if o.Progress != nil {
		o.Progress(line)
	}
}

// runAll executes each command; the first failure is the run's. It
// reports whether every command was a no-op, so a step can honestly
// say it was kept when the node already had the intended state.
func runAll(ctx context.Context, r preflight.Runner, exec [][]string, o Options) (bool, error) {
	kept := true
	for _, raw := range exec {
		// the plan's placeholders — the public key, the apply
		// timestamp — are substituted here, never persisted in the
		// plan itself
		argv := make([]string, len(raw))
		for j, a := range raw {
			argv[j] = expand(a, o)
		}
		// useradd is the one command that cannot re-run: an existing
		// user is kept as-is, and the step continues
		if argv[0] == "useradd" {
			if out, code, _ := runArgv(ctx, r, o.wrap("id", argv[len(argv)-1])); code == 0 && out != "" {
				o.report("kept · " + argv[len(argv)-1] + " already exists")
				continue
			}
		}
		if out, code, err := runArgv(ctx, r, o.wrap(argv...)); err != nil || code != 0 {
			return kept, fmt.Errorf("%s failed (%d): %s", strings.Join(argv, " "), code, firstLine(out))
		}
		kept = false
	}
	return kept, nil
}

// writeFile places one file: skipped when identical, backed up when
// different, written atomically otherwise. It reports whether the
// file was already in place. The remote read runs with the same
// privilege as the write so a root-only file is never mistaken for
// an absent one.
func writeFile(ctx context.Context, r preflight.Runner, f plan.File, o Options) (bool, error) {
	// the comparison is exact bytes: cat returns the file as it is,
	// and "already in place" must mean precisely that
	content := expand(f.Content, o)
	// presence is the exit code's to say, not the output's: a failed
	// cat folds its "No such file or directory" into the stream, and
	// the first write on a fresh node must never mistake that for
	// present-but-different content — it would try to back up a file
	// that does not exist and fail the whole apply
	current, catCode, _ := runArgv(ctx, r, o.wrap("cat", f.Path))
	if catCode != 0 {
		current = "" // absent or unreadable: the write below is the source of truth
	}
	if current == content {
		o.report("kept · " + f.Path + " already holds the intended content")
		return true, nil
	}
	if current != "" {
		// the exit code — not an error value — says whether a backup
		// exists; a missing backup is the one case where a new one
		// may be written, so the original survives every re-run
		if _, code, _ := runArgv(ctx, r, o.wrap("test", "-f", f.Path+".mymo-bak")); code != 0 {
			if _, code, err := runArgv(ctx, r, o.wrap("cp", f.Path, f.Path+".mymo-bak")); err != nil || code != 0 {
				return false, fmt.Errorf("could not back up %s before writing", f.Path)
			}
			o.report("backed up · " + f.Path + " → " + f.Path + ".mymo-bak")
		}
	}
	if _, code, err := runArgv(ctx, r, o.wrap("mkdir", "-p", path.Dir(f.Path))); err != nil || code != 0 {
		return false, fmt.Errorf("could not create the parent of %s", f.Path)
	}
	tmp := f.Path + ".mymo-new"
	script := fmt.Sprintf("printf %%s %s | base64 -d > %s && chmod %s %s && chown %s %s && mv %s %s",
		base64.StdEncoding.EncodeToString([]byte(content)), tmp, f.Mode, tmp, f.Owner, tmp, tmp, f.Path)
	if out, code, err := runArgv(ctx, r, o.wrap("sh", "-c", script)); err != nil || code != 0 {
		return false, fmt.Errorf("writing %s failed (%d): %s", f.Path, code, firstLine(out))
	}
	o.report("wrote · " + f.Path + " (" + f.Mode + ", " + f.Owner + ")")
	return false, nil
}

// expand substitutes the placeholders the plan left for apply-time
// material: the generated public key and the apply's timestamp.
func expand(s string, o Options) string {
	s = strings.ReplaceAll(s, pubKeyPlaceholder, o.MymoPublicKey)
	return strings.ReplaceAll(s, appliedAtPlaceholder, o.Now.UTC().Format(time.RFC3339))
}

// wrap prefixes one command with sudo -n when the operator is not
// root, so every mutation and every check carries the same
// privilege.
func (o Options) wrap(argv ...string) []string {
	if !o.Sudo {
		return argv
	}
	return append([]string{"sudo", "-n"}, argv...)
}

// firstLine keeps failure notes to one line.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return strings.TrimRight(s[:i], " \n")
	}
	return strings.TrimRight(s, " \n")
}

// MymoKeyUser is the account the generated key belongs to.
const MymoKeyUser = baseline.MymoUser
