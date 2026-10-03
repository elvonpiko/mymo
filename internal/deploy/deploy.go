// Package deploy runs the deployment lifecycle mymo promises:
//
//	CONFIRM → BUILD/PULL → STAGE → HEALTH CHECK → ACTIVATE →
//	VERIFY → KEEP PREVIOUS RELEASE AVAILABLE
//
// Its rules mirror the spec's failure semantics: the new release
// never replaces the current one until it is healthy and activated,
// every failure leaves the current release serving, and no outcome
// is claimed without verification. Containers never publish ports —
// they live on mymo's docker network, reached by name; the public
// path is Caddy alone.
//
// Networking: each release runs as mymo-<app>-r<id> — the container
// name is its unique DNS identity on the network, so health checks
// and Caddy routes address one release exactly, never a mix of two.
// Every running container also carries the stable <app> alias for
// app-to-app traffic; the only moment two containers share it is the
// blue-green window between staging and retirement, which affects
// internal-by-name traffic alone — public traffic switches at the
// Caddy reload, atomically.
package deploy

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// Runner executes one explicit-argument-vector command on the node.
type Runner = preflight.Runner

// NetworkName is the docker network every managed container joins.
// Ports are never published; the network is the only path in.
const NetworkName = "mymo-net"

// HealthImage is the throwaway prover for health checks. It runs
// --rm on the network, so the check does not depend on the app
// image containing a shell or a wget. Pinned — never "latest".
const HealthImage = "busybox:1.36"

// StepState is a deploy step's outcome, in the same language as the
// apply report: done, kept, failed, blocked.
type StepState string

const (
	StepDone    StepState = "done"
	StepKept    StepState = "kept"
	StepFailed  StepState = "failed"
	StepBlocked StepState = "blocked"
)

// StepResult is one step's row for the report.
type StepResult struct {
	Control string
	Title   string
	State   StepState
	Note    string
}

// Result is a deploy's whole truth: the step report, the updated
// application record — persisted by the caller on success AND
// failure, because failed releases are part of the history — and
// where it stopped when it stopped.
type Result struct {
	Steps    []StepResult
	App      domain.App
	OK       bool
	FailedAt string
}

// Copier transfers a build context to the node — a tarball over the
// ssh transport's stdin. The engine composes the destination; the
// transport decides how the bytes travel.
type Copier interface {
	Copy(ctx context.Context, dest string, tarball io.Reader) error
}

// Options carries the deploy's context: privilege, the ceremony
// sink, time, and the context transfer for dockerfile builds.
type Options struct {
	// Sudo prefixes every command with sudo -n when the operator
	// is not root — the same privilege rule as the apply engine.
	Sudo bool
	// Progress receives the ceremony lines as the deploy runs.
	Progress func(string)
	// Now stamps the release record.
	Now func() time.Time
	// Copier transfers build contexts; dockerfile deploys refuse
	// without it.
	Copier Copier
	// BuildContext is the local directory a dockerfile deploy
	// builds from, resolved by the caller.
	BuildContext string
	// HealthAttempts bounds the health polling per step.
	HealthAttempts int
	// HealthPause spaces health attempts.
	HealthPause time.Duration
}

// defaults fills the fields a caller may leave unset.
func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HealthAttempts < 1 {
		o.HealthAttempts = 15
	}
	if o.HealthPause < 0 {
		o.HealthPause = 2 * time.Second
	}
	return o
}

func (o Options) report(line string) {
	if o.Progress != nil {
		o.Progress(line)
	}
}

// wrap prefixes one command with sudo -n when the operator is not
// root.
func (o Options) wrap(argv ...string) []string {
	if !o.Sudo {
		return argv
	}
	return append([]string{"sudo", "-n"}, argv...)
}

// run executes one command through the runner, unwrapping the
// sudo decision. code carries the exit status; err means the
// command never completed.
func run(ctx context.Context, r Runner, o Options, argv ...string) (string, int, error) {
	return r.Run(ctx, argv[0], argv[1:]...)
}

// runW runs one command with the sudo decision applied.
func runW(ctx context.Context, r Runner, o Options, argv ...string) (string, int, error) {
	wrapped := o.wrap(argv...)
	return r.Run(ctx, wrapped[0], wrapped[1:]...)
}

// firstLine keeps failure notes to one row.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Deploy runs the lifecycle for one new release of app. The app
// record must already carry the manifest's truth; the node must be
// READY — the caller gates that, the engine never assumes it.
func Deploy(ctx context.Context, r Runner, app domain.App, o Options) Result {
	o = o.withDefaults()
	res := Result{App: app}
	id := app.NextReleaseID()
	container := domain.ContainerName(app.Name, id)
	now := o.Now().UTC()

	step := func(control, title string) *StepResult {
		res.Steps = append(res.Steps, StepResult{Control: control, Title: title, State: StepBlocked})
		return &res.Steps[len(res.Steps)-1]
	}
	fail := func(step *StepResult, note string, control string) Result {
		step.State = StepFailed
		step.Note = note
		res.OK = false
		res.FailedAt = control
		res.App.Releases = append(res.App.Releases, domain.Release{
			ID: id, Source: sourceLine(app, o), Container: container,
			Health: "failed: " + note, State: domain.ReleaseFailed, DeployedAt: now,
		})
		res.App.Releases = capReleases(res.App.Releases)
		return res
	}

	// 1 — the network every release joins
	net := step("network", "docker network")
	if _, code, err := runW(ctx, r, o, "docker", "network", "inspect", NetworkName); err != nil {
		return fail(net, "the network check never completed: "+err.Error(), "network")
	} else if code != 0 {
		if out, code, err := runW(ctx, r, o, "docker", "network", "create", NetworkName); err != nil || code != 0 {
			return fail(net, fmt.Sprintf("creating %s failed (%d): %s", NetworkName, code, firstLine(out)), "network")
		}
		net.State, net.Note = StepDone, NetworkName+" created — the only path to managed containers"
	} else {
		net.State, net.Note = StepKept, NetworkName+" already exists"
	}

	// 2 — the artifact: pull a registry image, or build on the node
	imageRef := app.Image
	digest := ""
	if app.Type == domain.SourceImage {
		pull := step("pull", "image "+app.Image)
		o.report("pulling " + app.Image)
		out, code, err := runW(ctx, r, o, "docker", "pull", app.Image)
		if err != nil || code != 0 {
			return fail(pull, fmt.Sprintf("pull failed (%d): %s", code, firstLine(out)), "pull")
		}
		var ierr error
		digest, ierr = imageDigest(ctx, r, o, app.Image)
		if ierr != nil {
			return fail(pull, ierr.Error(), "pull")
		}
		pull.State, pull.Note = StepDone, "pulled; digest "+shortDigest(digest)
	} else {
		build := step("build", "dockerfile "+app.Dockerfile)
		if o.Copier == nil || o.BuildContext == "" {
			return fail(build, "the dockerfile strategy needs a build context transfer, which is not available", "build")
		}
		dest := fmt.Sprintf("/tmp/mymo-build/%s-r%d", app.Name, id)
		if out, code, err := runW(ctx, r, o, "mkdir", "-p", dest); err != nil || code != 0 {
			return fail(build, "preparing the build directory failed: "+firstLine(out), "build")
		}
		tarball, terr := archiveBuildContext(o.BuildContext)
		if terr != nil {
			return fail(build, "packing the build context failed: "+terr.Error(), "build")
		}
		if cerr := o.Copier.Copy(ctx, dest, tarball); cerr != nil {
			return fail(build, "transferring the build context failed: "+cerr.Error(), "build")
		}
		imageRef = fmt.Sprintf("mymo/%s:r%d", app.Name, id)
		o.report("building " + imageRef + " on the node")
		out, code, err := runW(ctx, r, o, "docker", "build", "-t", imageRef, dest)
		if err != nil || code != 0 {
			runW(ctx, r, o, "rm", "-rf", dest) // best effort; the build dir is scratch
			return fail(build, fmt.Sprintf("build failed (%d): %s", code, firstLine(out)), "build")
		}
		runW(ctx, r, o, "rm", "-rf", dest)
		var ierr error
		digest, ierr = imageDigest(ctx, r, o, imageRef)
		if ierr != nil {
			return fail(build, ierr.Error(), "build")
		}
		build.State, build.Note = StepDone, "built on the node; digest "+shortDigest(digest)
	}

	// 3 — stage: clean up failed releases' containers, then start
	// the new one on the network, under no published port ever
	stage := step("stage", "release r"+fmt.Sprint(id))
	for _, old := range app.Releases {
		if old.State == domain.ReleaseFailed && old.Container != "" {
			if _, code, err := runW(ctx, r, o, "docker", "rm", old.Container); err == nil && code == 0 {
				o.report("removed the failed r" + fmt.Sprint(old.ID) + " container — its logs had their turn")
			}
		}
	}
	runArgv := []string{"docker", "run", "-d",
		"--name", container,
		"--network", NetworkName,
		"--network-alias", app.Name,
		"--restart", "unless-stopped",
		imageRef}
	if out, code, err := runW(ctx, r, o, runArgv...); err != nil || code != 0 {
		return fail(stage, fmt.Sprintf("the container did not start (%d): %s", code, firstLine(out)), "stage")
	}
	stage.State = StepDone
	stage.Note = container + " is running on " + NetworkName + " — no port is published"

	// 4 — health: what the app declared, verified by mymo
	health := step("health", "health check")
	healthNote, hErr := probeHealth(ctx, r, o, app, container)
	if hErr != nil {
		// the new release stays stopped-but-present for its logs;
		// the current release never even noticed
		runW(ctx, r, o, "docker", "stop", container)
		return fail(health, hErr.Error(), "health")
	}
	health.State = StepDone
	health.Note = healthNote

	// 5 — activate: the caddy route swaps releases only when the
	// new config validates; the previous route survives any refusal
	activate := step("activate", "caddy route")
	if app.Domain == "" {
		activate.State, activate.Note = StepKept, "internal only — there is no route to activate"
	} else {
		swapped, aErr := activateRoute(ctx, r, o, domainApp{Name: app.Name, Domain: app.Domain, Port: app.Port}, container)
		if aErr != nil {
			// the route still serves the old release; the new one
			// is stopped and kept for its logs
			runW(ctx, r, o, "docker", "stop", container)
			return fail(activate, aErr.Error(), "activate")
		}
		activate.State = StepDone
		activate.Note = swapped
	}

	// 6 — retire the previous release: stopped, kept for rollback;
	// anything older is pruned, its record staying behind
	prev := step("previous", "previous release")
	if old, ok := app.ActiveRelease(); ok {
		if out, code, err := runW(ctx, r, o, "docker", "stop", old.Container); err != nil || code != 0 {
			// the swap already happened; failing the deploy here
			// would be dishonest. It is a leak, not a rollback.
			prev.State, prev.Note = StepFailed, "could not stop "+old.Container+": "+firstLine(out)+" — it keeps running; the next deploy will try again"
		} else {
			prev.State, prev.Note = StepDone, old.Container+" stopped and kept for rollback"
		}
		for i := range res.App.Releases {
			if res.App.Releases[i].ID == old.ID {
				res.App.Releases[i].State = domain.ReleaseRetired
			}
		}
	} else {
		prev.State, prev.Note = StepKept, "first release — nothing to retire"
	}
	// prune: containers older than the previous release
	for _, old := range prunable(res.App.Releases, app.Name) {
		if _, code, err := runW(ctx, r, o, "docker", "rm", old); err == nil && code == 0 {
			o.report("pruned " + old + " — older than the rollback window")
		} else if err != nil {
			o.report("could not prune " + old + ": " + err.Error())
		}
	}

	// 7 — verify: running, healthy, and the public path honest
	verify := step("verify", "verification")
	if out, code, err := runW(ctx, r, o, "docker", "ps", "--filter", "name="+container,
		"--filter", "status=running", "--format", "{{.Names}}"); err != nil || code != 0 || strings.TrimSpace(out) != container {
		return fail(verify, "the container is not running after activation", "verify")
	}
	if note, hErr := probeHealth(ctx, r, o, app, container); hErr != nil {
		return fail(verify, "the health check stopped passing after activation: "+hErr.Error(), "verify")
	} else if app.Health != "" {
		verify.Note = note
	}
	if app.Domain != "" {
		if out, _, err := runW(ctx, r, o, "systemctl", "is-active", "caddy"); err != nil || strings.TrimSpace(out) != "active" {
			return fail(verify, "caddy is not active after the reload", "verify")
		}
		verify.Note = "the release serves; caddy reloaded the route cleanly"
	}
	if verify.Note == "" {
		verify.Note = "the container runs, healthy inside the network"
	}
	verify.State = StepDone

	// the record: immutable release, active
	res.App.Releases = append(res.App.Releases, domain.Release{
		ID: id, Digest: digest, Source: sourceLine(app, o), Container: container,
		Health: healthNote, State: domain.ReleaseActive, DeployedAt: now,
	})
	res.App.Releases = capReleases(res.App.Releases)
	res.OK = true
	return res
}

// Rollback selects the most recent retired release and makes it the
// active one. It is deterministic: the recorded container is started
// again — no rebuild, no new digest — and the route, if any, points
// back at it. The current release is retired, not destroyed.
func Rollback(ctx context.Context, r Runner, app domain.App, o Options) Result {
	o = o.withDefaults()
	res := Result{App: app}
	step := func(control, title string) *StepResult {
		res.Steps = append(res.Steps, StepResult{Control: control, Title: title, State: StepBlocked})
		return &res.Steps[len(res.Steps)-1]
	}
	fail := func(step *StepResult, note, control string) Result {
		step.State, step.Note = StepFailed, note
		res.OK, res.FailedAt = false, control
		return res
	}

	target, ok := app.RollbackTarget()
	if !ok {
		s := step("rollback", "rollback")
		return fail(s, "there is no retired release to roll back to", "rollback")
	}
	current, hasCurrent := app.ActiveRelease()

	start := step("rollback", "release r"+fmt.Sprint(target.ID))
	if out, code, err := runW(ctx, r, o, "docker", "start", target.Container); err != nil || code != 0 {
		return fail(start, fmt.Sprintf("r%d did not start again (%d): %s", target.ID, code, firstLine(out)), "rollback")
	}
	healthNote, hErr := probeHealth(ctx, r, o, app, target.Container)
	if hErr != nil {
		runW(ctx, r, o, "docker", "stop", target.Container)
		return fail(start, "the rollback target failed its health check: "+hErr.Error(), "rollback")
	}
	start.State, start.Note = StepDone, "r"+fmt.Sprint(target.ID)+" runs again — "+healthNote

	if app.Domain != "" {
		route := step("rollback", "caddy route")
		if swapped, err := activateRoute(ctx, r, o, domainApp{Name: app.Name, Domain: app.Domain, Port: app.Port}, target.Container); err != nil {
			runW(ctx, r, o, "docker", "stop", target.Container)
			return fail(route, err.Error(), "route")
		} else {
			route.State, route.Note = StepDone, swapped
		}
	}

	if hasCurrent {
		stop := step("rollback", "release r"+fmt.Sprint(current.ID))
		if out, code, err := runW(ctx, r, o, "docker", "stop", current.Container); err != nil || code != 0 {
			return fail(stop, fmt.Sprintf("r%d did not stop: %s", current.ID, firstLine(out)), "rollback")
		}
		stop.State, stop.Note = StepDone, current.Container+" stopped and kept"
	}

	for i := range res.App.Releases {
		switch res.App.Releases[i].ID {
		case target.ID:
			res.App.Releases[i].State = domain.ReleaseActive
			res.App.Releases[i].Health = "rolled back at " + o.Now().UTC().Format(time.RFC3339)
		case current.ID:
			res.App.Releases[i].State = domain.ReleaseRetired
		}
	}
	res.OK = true
	return res
}

// probeHealth verifies what the app declared. Without a declared
// path, a running container is all mymo can honestly vouch for —
// and the note says so instead of claiming health.
func probeHealth(ctx context.Context, r Runner, o Options, app domain.App, container string) (string, error) {
	if app.Health == "" {
		return "no health endpoint declared — the container running is all mymo can vouch for", nil
	}
	url := fmt.Sprintf("http://%s:%d%s", container, app.Port, app.Health)
	for attempt := 1; attempt <= o.HealthAttempts; attempt++ {
		if out, code, err := runW(ctx, r, o, "docker", "run", "--rm", "--network", NetworkName,
			HealthImage, "wget", "-q", "-T", "2", "-O", "/dev/null", url); err == nil && code == 0 {
			return app.Health + " answers on the network", nil
		} else if attempt == o.HealthAttempts {
			return "", fmt.Errorf("%s never answered (last attempt: %s)", url, firstLine(out))
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%s was never answered: %v", url, ctx.Err())
		case <-time.After(o.HealthPause):
		}
	}
	return "", fmt.Errorf("%s never answered", url)
}

// imageDigest resolves a reference to its immutable sha256 identity.
func imageDigest(ctx context.Context, r Runner, o Options, ref string) (string, error) {
	out, code, err := runW(ctx, r, o, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil || code != 0 {
		return "", fmt.Errorf("could not resolve the digest of %s: %s", ref, firstLine(out))
	}
	d := strings.TrimSpace(out)
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("%s resolved to %q, which is not a digest", ref, d)
	}
	return d, nil
}

// shortDigest keeps reports to one row.
func shortDigest(d string) string {
	if len(d) > len("sha256:")+7 {
		return d[:len("sha256:")+7] + "…"
	}
	return d
}

// sourceLine records where the artifact came from.
func sourceLine(app domain.App, o Options) string {
	if app.Type == domain.SourceImage {
		return "image " + app.Image
	}
	return "dockerfile " + app.Dockerfile
}

// capReleases keeps the history bounded: the last 10 records stay,
// the oldest fall off. The running truth is never older than the
// records.
func capReleases(rs []domain.Release) []domain.Release {
	const keep = 10
	if len(rs) <= keep {
		return rs
	}
	return append([]domain.Release(nil), rs[len(rs)-keep:]...)
}

// prunable names the containers older than the rollback window: a
// release is kept while it is active or the most recent retired one;
// anything older had its turn.
func prunable(rs []domain.Release, appName string) []string {
	// find the most recent retired release — the rollback window's
	// lower edge
	lastRetired := -1
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].State == domain.ReleaseRetired {
			lastRetired = i
			break
		}
	}
	var out []string
	for i, r := range rs {
		if r.Container == "" || i >= lastRetired {
			continue
		}
		if r.State == domain.ReleaseRetired || r.State == domain.ReleaseFailed {
			out = append(out, r.Container)
		}
	}
	return out
}
