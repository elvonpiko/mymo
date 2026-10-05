package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/manifest"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
)

// deployConfirmIn is the confirmation prompt's input; a var so
// tests can refuse the way pipelines do — EOF and anything but the
// app's exact name aborts, and nothing changes.
var deployConfirmIn io.Reader = os.Stdin

// deployDial opens the runner and the context copier for a node; a
// var so tests drive the whole command hermetically. The close func
// releases the connection when the deploy is done with it.
var deployDial = func(ctx context.Context, node domain.Node, store *state.Store, progress func(string)) (r deploy.Runner, sudo func(bool) deploy.Copier, closeFn func() error, err error) {
	client := ssh.New(node, knownHostsPath(store))
	client.WithProgress(progress)
	if err := client.Dial(ctx); err != nil {
		return nil, nil, nil, err
	}
	return client,
		func(sudo bool) deploy.Copier { return sshCopier{client: client, sudo: sudo} },
		client.Close,
		nil
}

// defaultDeployDial keeps the real transport for test cleanup.
var defaultDeployDial = deployDial

// sshCopier carries build contexts through the ssh transport.
type sshCopier struct {
	client *ssh.Client
	sudo   bool
}

func (c sshCopier) Copy(ctx context.Context, dest string, tarball io.Reader) error {
	return c.client.Copy(ctx, dest, tarball, c.sudo)
}

// runDeploy deploys the project in the working directory: it
// discovers the source (mymo.yaml is the truth, a bare Dockerfile
// earns a draft to finish), resolves the target app and node, asks
// for the app's name typed in full as the human confirmation, then
// runs the release lifecycle with visible progress. The record is
// written on success and failure alike — history is history.
func runDeploy(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var nodeFlag string
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		switch {
		case rest[0] == "-node" && len(rest) > 1:
			nodeFlag = rest[1]
			rest = rest[2:]
		default:
			fmt.Fprintf(stderr, "mymo deploy: unknown flag %q\n\n%s", rest[0], deployUsage())
			return exitUsage
		}
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "usage: mymo deploy -node <name>\n\nrun it in the project directory")
		return exitUsage
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		return exitErr
	}
	m, draft, found, err := manifest.Discover(cwd)
	if err != nil {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		if errors.Is(err, manifest.ErrNoSource) {
			fmt.Fprintln(stderr, "\nsupported deployment modes:")
			fmt.Fprintln(stderr, "  mymo.yaml with type: image       — deploy an existing OCI image")
			fmt.Fprintln(stderr, "  mymo.yaml with type: dockerfile — build the project's Dockerfile on the node")
		}
		return exitErr
	}
	if draft != "" {
		fmt.Fprintf(stderr, "mymo deploy: this project has a %s but no %s, and mymo does not guess\n", found, manifest.FileName)
		fmt.Fprintf(stderr, "\nwrite this draft to %s, finish it, and run again:\n\n%s\n", manifest.FileName, draft)
		return exitErr
	}

	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}

	// the app record: existing apps keep their node and identity;
	// a new app names its node now
	app, err := store.GetApp(m.Name)
	existing := err == nil
	if err != nil && !errors.Is(err, state.ErrAppNotFound) {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		return exitErr
	}
	if existing {
		if nodeFlag != "" && nodeFlag != app.Node {
			fmt.Fprintf(stderr, "mymo deploy: app %q lives on node %q — an app does not change nodes\n", app.Name, app.Node)
			return exitErr
		}
		if string(app.Type) != m.Type {
			fmt.Fprintf(stderr, "mymo deploy: app %q is type %s; changing to %s is not supported — remove and redeploy under a new name\n", app.Name, app.Type, m.Type)
			return exitErr
		}
	}
	if !existing {
		if nodeFlag == "" {
			fmt.Fprintf(stderr, "mymo deploy: a new application needs its node — mymo deploy -node <name>\n")
			return exitErr
		}
		app = domain.App{Name: m.Name, Node: nodeFlag, Type: domain.SourceImage, AddedAt: time.Now().UTC()}
	}
	node, err := store.GetNode(app.Node)
	if err != nil {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		return exitErr
	}
	if node.Bootstrap.State != domain.BootstrapReady {
		fmt.Fprintf(stderr, "mymo deploy: node %s is %s, not prepared — mymo cannot deploy to a node it has not verified\n", node.Name, bootstrapStateWord(node.Bootstrap.State))
		return exitErr
	}

	// the manifest updates the app's deployable truth; the releases
	// history is untouched by it
	app.Type = domain.AppSource(m.Type)
	app.Image = m.Image
	app.Dockerfile = m.Dockerfile
	app.Port = m.Port
	app.Domain = m.Domain
	app.Health = m.Health
	if err := app.Validate(); err != nil {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		return exitErr
	}

	expose := app.Domain
	if expose == "" {
		expose = "internal only"
	}
	artifact := app.Image
	if app.Type == domain.SourceDockerfile {
		artifact = "Dockerfile in " + app.Dockerfile
	}
	fmt.Fprintf(stdout, "deploy · %s → %s\n", app.Name, app.Node)
	fmt.Fprintf(stdout, "  type     %s · %s\n", app.Type, artifact)
	fmt.Fprintf(stdout, "  exposure %s · internal port %d\n", expose, app.Port)
	if app.Health != "" {
		fmt.Fprintf(stdout, "  health   %s\n", app.Health)
	}
	if rel, ok := app.ActiveRelease(); ok {
		fmt.Fprintf(stdout, "  current  release %d (%s)\n", rel.ID, rel.Health)
	}
	fmt.Fprintf(stdout, "\ntype %s to deploy — anything else aborts\n", app.Name)

	confirm := bufio.NewScanner(deployConfirmIn)
	if !confirm.Scan() || strings.TrimSpace(confirm.Text()) != app.Name {
		fmt.Fprintf(stdout, "aborted — %s is untouched\n", app.Name)
		return exitErr
	}

	runner, copierFor, closeFn, err := deployDial(ctx, node, store, dialNarrator(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "mymo deploy: %v\n", err)
		return exitErr
	}
	defer closeFn()

	// privilege: the operator is root or is not; the engine's every
	// command carries the answer
	sudo := false
	if out, _, err := runner.Run(ctx, "id", "-u"); err == nil && strings.TrimSpace(out) != "0" {
		sudo = true
	}

	opts := deploy.Options{
		Sudo:     sudo,
		Progress: func(line string) { fmt.Fprintln(stdout, "· "+line) },
		Copier:   copierFor(sudo),
	}
	if app.Type == domain.SourceDockerfile {
		// the build context is the manifest's directory, resolved
		// against the project — mymo was run from its root
		opts.BuildContext, err = filepath.Abs(app.Dockerfile)
		if err != nil {
			fmt.Fprintf(stderr, "mymo deploy: resolving the build context: %v\n", err)
			return exitErr
		}
	}

	res := deploy.Deploy(ctx, runner, app, opts)

	// history is history: the record is written for failures too
	if existing {
		err = store.UpdateApp(res.App)
	} else {
		err = store.AddApp(res.App)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mymo deploy: recording the release: %v\n", err)
		return exitErr
	}

	printDeployReport(stdout, res)
	if !res.OK {
		if _, wasServing := res.App.ActiveRelease(); wasServing {
			fmt.Fprintf(stderr, "mymo deploy: failed at %s — the current release keeps serving\n", res.FailedAt)
		} else {
			fmt.Fprintf(stderr, "mymo deploy: failed at %s — nothing was activated\n", res.FailedAt)
		}
		return exitErr
	}

	rel, _ := res.App.ActiveRelease()
	fmt.Fprintf(stdout, "\n%s release %d is live on %s", app.Name, rel.ID, node.Name)
	if app.Domain != "" {
		fmt.Fprintf(stdout, " · https://%s\n", app.Domain)
	} else {
		fmt.Fprintf(stdout, " — reachable inside the network as %s:%d\n", app.Name, app.Port)
	}
	fmt.Fprintf(stdout, "rollback selects %s: mymo app rollback %s\n", rollbackHint(res.App), app.Name)
	return exitOK
}

// rollbackHint names what a rollback would select, honestly —
// including when there is nothing to select.
func rollbackHint(app domain.App) string {
	if target, ok := app.RollbackTarget(); ok {
		return fmt.Sprintf("release %d", target.ID)
	}
	return "nothing yet — the first release has no predecessor"
}

// printDeployReport renders the step rows in the apply report's
// language: done, kept, FAILED with the reason, blocked never run.
func printDeployReport(w io.Writer, res deploy.Result) {
	fmt.Fprintln(w)
	for _, s := range res.Steps {
		switch s.State {
		case deploy.StepDone:
			fmt.Fprintf(w, "  done    %s — %s\n", s.Title, s.Note)
		case deploy.StepKept:
			fmt.Fprintf(w, "  kept    %s — %s\n", s.Title, s.Note)
		case deploy.StepFailed:
			fmt.Fprintf(w, "  FAILED  %s — %s\n", s.Title, s.Note)
		case deploy.StepBlocked:
			fmt.Fprintf(w, "  blocked %s\n", s.Title)
		}
	}
}

// bootstrapStateWord says a state in one plain word.
func bootstrapStateWord(s domain.BootstrapState) string {
	switch s {
	case domain.BootstrapReady:
		return "ready"
	case domain.BootstrapApplying:
		return "mid-apply"
	case domain.BootstrapPlan, domain.BootstrapConfirmed:
		return "planned, not applied"
	case domain.BootstrapPreflight:
		return "preflighted, not planned"
	default:
		return "not preflighted"
	}
}

func deployUsage() string {
	return "usage: mymo deploy -node <name>\n\nrun it in the project directory"
}
