package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// opsDial opens the runner for an app operation; a var so tests
// drive the commands hermetically.
var opsDial = func(ctx context.Context, node domain.Node, store *state.Store, progress func(string)) (deploy.Runner, func() error, error) {
	client := ssh.New(node, knownHostsPath(store))
	client.WithProgress(progress)
	if err := client.Dial(ctx); err != nil {
		return nil, nil, err
	}
	return client, client.Close, nil
}

// defaultOpsDial keeps the real transport for test cleanup.
var defaultOpsDial = opsDial

// loadApp resolves an app and its node, refusing honestly at each
// step. ready gates mutations; logs and status only need a node
// that answers.
func loadApp(store *state.Store, name string, needReady bool, stderr io.Writer) (domain.App, domain.Node, bool) {
	app, err := store.GetApp(name)
	if err != nil {
		fmt.Fprintf(stderr, "mymo app: %v\n", err)
		return domain.App{}, domain.Node{}, false
	}
	node, err := store.GetNode(app.Node)
	if err != nil {
		fmt.Fprintf(stderr, "mymo app: %v\n", err)
		return domain.App{}, domain.Node{}, false
	}
	if needReady && node.Bootstrap.State != domain.BootstrapReady {
		fmt.Fprintf(stderr, "mymo app: node %s is %s — mymo only operates on a node it has verified\n", node.Name, bootstrapStateWord(node.Bootstrap.State))
		return domain.App{}, domain.Node{}, false
	}
	return app, node, true
}

// runAppStatus prints the app's record — exposure, releases with
// their digests and states — and asks the node what the active
// container is doing right now, because status is a question about
// reality, not about the record.
func runAppStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo app status <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	app, node, ok := loadApp(store, args[0], false, stderr)
	if !ok {
		return exitErr
	}

	artifact := app.Image
	if app.Type == domain.SourceDockerfile {
		artifact = "Dockerfile in " + app.Dockerfile
	}
	if app.Type == domain.SourceCompose {
		if cf, err := deploy.ParseCompose(app.Compose); err == nil {
			artifact = fmt.Sprintf("%s exposed · %d services: %s", app.Service, len(cf.Services), strings.Join(cf.Services, ", "))
		} else {
			artifact = app.Service + " exposed"
		}
	}
	fmt.Fprintf(stdout, "%s · node %s\n", app.Name, node.Name)
	fmt.Fprintf(stdout, "  type     %s · %s\n", app.Type, artifact)
	if app.Domain != "" {
		fmt.Fprintf(stdout, "  exposure https://%s → %s:%d\n", app.Domain, app.Name, app.Port)
	} else {
		fmt.Fprintf(stdout, "  exposure internal only → %s:%d\n", app.Name, app.Port)
	}

	if len(app.Releases) == 0 {
		fmt.Fprintln(stdout, "  releases none yet — nothing has been deployed")
		return exitOK
	}
	fmt.Fprintln(stdout, "  releases")
	for _, r := range app.Releases {
		state := string(r.State)
		switch r.State {
		case domain.ReleaseActive:
			state = "active"
		case domain.ReleaseRetired:
			state = "kept for rollback"
		case domain.ReleaseFailed:
			state = "failed"
		case domain.ReleaseGone:
			state = "gone from the box"
		}
		fmt.Fprintf(stdout, "    r%-3d %-16s %s\n", r.ID, state, releaseLine(r))
	}

	// the live word: the release the record claims, asked of the
	// box — and the record follows the box's authoritative answer
	rel, hasCurrent := app.CurrentRelease()
	if !hasCurrent {
		return exitOK
	}
	runner, closeFn, err := opsDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stdout, "  live     unknown — the node did not answer (%v)\n", err)
		return exitOK
	}
	defer closeFn()
	sudo := operatorIsNotRoot(ctx, runner)
	ops := app.Ops(rel)
	out, code, err := runSudo(ctx, runner, sudo, ops.Status[0], ops.Status[1:]...)
	reconciled := func(newApp domain.App) {
		if err := store.UpdateApp(newApp); err != nil {
			fmt.Fprintf(stderr, "mymo app status: reconciling the record: %v\n", err)
		}
	}
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "  live     unknown — the node did not answer (%v)\n", err)
	case code != 0:
		fmt.Fprintf(stdout, "  live     unknown — docker refused\n")
	case statusLineSays(out, ops.StatusAnswer):
		fmt.Fprintf(stdout, "  live     %s is running right now\n", ops.StatusAnswer)
		if rel.State == domain.ReleaseGone {
			// a container started back by hand: the box wins both
			// ways, and the record follows it back
			for i := range app.Releases {
				if app.Releases[i].ID == rel.ID {
					app.Releases[i].State = domain.ReleaseActive
					app.Releases[i].Health = "back on the box at " + time.Now().UTC().Format(time.RFC3339)
				}
			}
			reconciled(app)
			fmt.Fprintf(stdout, "  record   r%d runs again — the record follows the box\n", rel.ID)
		}
	default:
		fmt.Fprintf(stdout, "  live     %s is NOT running\n", ops.StatusAnswer)
		if rel.State == domain.ReleaseActive {
			// docker answered authoritatively: the release the
			// record claims is not on the box — the record follows
			for i := range app.Releases {
				if app.Releases[i].ID == rel.ID {
					app.Releases[i].State = domain.ReleaseGone
					app.Releases[i].Health = "gone from the box at " + time.Now().UTC().Format(time.RFC3339)
				}
			}
			reconciled(app)
			fmt.Fprintf(stdout, "  record   r%d is marked gone — the record follows the box; a redeploy is the recovery\n", rel.ID)
		}
	}
	return exitOK
}

// releaseLine says what a release was and how it ended, in one row:
// where its artifact came from, its immutable digest, its health at
// activation, and when.
func releaseLine(r domain.Release) string {
	var parts []string
	if r.Source != "" {
		parts = append(parts, r.Source)
	}
	if r.Digest != "" {
		parts = append(parts, shortDigest(r.Digest))
	} else if r.Source == "" {
		parts = append(parts, "no artifact resolved")
	}
	if r.Health != "" {
		parts = append(parts, r.Health)
	}
	parts = append(parts, ageWord(r.DeployedAt))
	return strings.Join(parts, " · ")
}

// runAppLogs prints the active release's container logs. The
// record says what is live; the container says what happened.
func runAppLogs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	tail := "100"
	if len(args) == 2 && args[0] == "-lines" {
		if n, err := strconv.Atoi(args[1]); err == nil && n > 0 {
			tail = args[1]
		} else {
			fmt.Fprintln(stderr, "usage: mymo app logs <name> [-lines N]")
			return exitUsage
		}
		args = nil
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo app logs <name> [-lines N]")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	app, node, ok := loadApp(store, args[0], false, stderr)
	if !ok {
		return exitErr
	}
	rel, hasCurrent := app.CurrentRelease()
	if !hasCurrent {
		fmt.Fprintf(stderr, "mymo app logs: %s has no release to read logs from\n", app.Name)
		return exitErr
	}
	runner, closeFn, err := opsDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "mymo app logs: %v\n", err)
		return exitErr
	}
	defer closeFn()
	// the tail count is the operator's, not the verb's: swap the
	// baked-in 100 for whatever was asked
	ops := app.Ops(rel)
	for i := range ops.Logs {
		if ops.Logs[i] == "100" {
			ops.Logs[i] = tail
		}
	}
	out, code, err := runSudo(ctx, runner, operatorIsNotRoot(ctx, runner), ops.Logs[0], ops.Logs[1:]...)
	if err != nil || code != 0 {
		fmt.Fprintf(stderr, "mymo app logs: the node refused (exit %d)\n", code)
		return exitErr
	}
	fmt.Fprint(stdout, out)
	return exitOK
}

// runAppLifecycle runs one container verb against the active
// release: restart, stop, start. The command is the intent; the
// engine's protections belong to deploys and rollbacks.
func runAppLifecycle(ctx context.Context, verb, name string, stdout, stderr io.Writer) int {
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	app, node, ok := loadApp(store, name, true, stderr)
	if !ok {
		return exitErr
	}
	rel, hasCurrent := app.CurrentRelease()
	if !hasCurrent {
		fmt.Fprintf(stderr, "mymo app %s: %s has no release to address\n", verb, app.Name)
		return exitErr
	}
	runner, closeFn, err := opsDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "mymo app %s: %v\n", verb, err)
		return exitErr
	}
	defer closeFn()
	if out, code, err := runSudo(ctx, runner, operatorIsNotRoot(ctx, runner), "docker", verb, rel.Container); err != nil || code != 0 {
		fmt.Fprintf(stderr, "mymo app %s: the node refused (exit %d): %s\n", verb, code, firstLine(out))
		return exitErr
	}
	past := map[string]string{"restart": "restarted", "stop": "stopped", "start": "started"}[verb]
	fmt.Fprintf(stdout, "%s %s\n", rel.Container, past)
	return exitOK
}

// runAppRollback selects the previous release and makes it live —
// deterministically, no rebuild. The app's name typed in full is the
// confirmation, the same gate as a deploy.
func runAppRollback(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo app rollback <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	app, node, ok := loadApp(store, args[0], true, stderr)
	if !ok {
		return exitErr
	}
	target, hasTarget := app.RollbackTarget()
	if !hasTarget {
		fmt.Fprintf(stderr, "mymo app rollback: %s has no retired release to roll back to\n", app.Name)
		return exitErr
	}
	current, _ := app.CurrentRelease()

	fmt.Fprintf(stdout, "rollback · %s · release %d → release %d\n", app.Name, current.ID, target.ID)
	fmt.Fprintf(stdout, "  restoring %s from %s\n", target.Container, shortDigest(target.Digest))
	fmt.Fprintf(stdout, "\ntype %s to roll back — anything else aborts\n", app.Name)

	confirm := bufio.NewScanner(deployConfirmIn)
	if !confirm.Scan() || strings.TrimSpace(confirm.Text()) != app.Name {
		fmt.Fprintf(stdout, "aborted — %s keeps serving release %d\n", app.Name, current.ID)
		return exitErr
	}

	runner, closeFn, err := opsDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "mymo app rollback: %v\n", err)
		return exitErr
	}
	defer closeFn()
	res := deploy.Rollback(ctx, runner, app, deploy.Options{
		Sudo:     operatorIsNotRoot(ctx, runner),
		Progress: func(line string) { fmt.Fprintln(stdout, "· "+line) },
	})
	if err := store.UpdateApp(res.App); err != nil {
		fmt.Fprintf(stderr, "mymo app rollback: recording: %v\n", err)
		return exitErr
	}
	printDeployReport(stdout, res)
	if !res.OK {
		fmt.Fprintf(stderr, "mymo app rollback: failed — release %d keeps serving\n", current.ID)
		return exitErr
	}
	rel, _ := res.App.ActiveRelease()
	fmt.Fprintf(stdout, "\n%s release %d is live again on %s\n", app.Name, rel.ID, node.Name)
	return exitOK
}

// runAppShell opens a real shell inside the active release's
// container — the operator's terminal handed to docker exec
// through ssh, not emulated anywhere.
func runAppShell(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo app shell <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	app, node, ok := loadApp(store, args[0], false, stderr)
	if !ok {
		return exitErr
	}
	rel, hasCurrent := app.CurrentRelease()
	if !hasCurrent {
		fmt.Fprintf(stderr, "mymo app shell: %s has no release to shell into\n", app.Name)
		return exitErr
	}

	// privilege: ask the node who is connected
	runner, closeFn, err := opsDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "mymo app shell: %v\n", err)
		return exitErr
	}
	sudo := operatorIsNotRoot(ctx, runner)
	closeFn()

	ops := app.Ops(rel)
	cmd, err := ssh.ExecCommand(node, ssh.InteractiveKnownHostsPath(store.Dir()), sudo, append([]string{ops.Shell[0]}, ops.Shell[1:]...)...)
	if err != nil {
		fmt.Fprintf(stderr, "mymo app shell: %v\n", err)
		return exitErr
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintf(stderr, "mymo app shell: no ssh executable in PATH\n")
		}
		return exitErr
	}
	return exitOK
}

// operatorIsNotRoot asks the node who mymo connected as; sudo -n
// carries every command that needs privilege when it is not root.
func operatorIsNotRoot(ctx context.Context, r deploy.Runner) bool {
	out, _, err := r.Run(ctx, "id", "-u")
	return err == nil && strings.TrimSpace(out) != "0"
}

// runSudo runs one command through the privilege decision.
func runSudo(ctx context.Context, r deploy.Runner, sudo bool, name string, args ...string) (string, int, error) {
	if sudo {
		return r.Run(ctx, "sudo", append([]string{"-n", name}, args...)...)
	}
	return r.Run(ctx, name, args...)
}

func shortDigest(d string) string {
	if len(d) > len("sha256:")+7 {
		return d[:len("sha256:")+7] + "…"
	}
	return d
}

// ageWord says a timestamp's age the way the fleet does.
func ageWord(t time.Time) string {
	if t.IsZero() {
		return "unknown when"
	}
	return facts.FormatAge(t)
}

// firstLine keeps failure notes to one row.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// statusLineSays reports whether the status command's output
// carries the expected answer as one of its lines.
func statusLineSays(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
