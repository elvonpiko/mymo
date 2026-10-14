package deploy

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
)

// The compose deploy path. mymo does not replace Docker Compose —
// it runs the operator's file verbatim, beside its own override:
//
//	/var/lib/mymo/compose/<app>/r<id>.yaml        the operator's file, as deployed
//	/var/lib/mymo/compose/<app>/r<id>.mymo.yaml   mymo's override: the network, the pin, the release's image tags
//
// The override is how mymo owns the production lifecycle without
// touching the operator's yaml: it joins every built service to
// the release's own image tag, attaches the exposed service to
// mymo's network with the pinned address, and names mymo's own
// network external so compose never invents a second one. The
// pair together is a release's exact configuration — rollback
// replays both, so the old release runs the old images, not the
// new build under an old tag.
//
// The lifecycle differs from a single container's, honestly: up
// recreates the services that changed, so there is no stop-the-
// old step — the previous release is replaced in place, and its
// configuration stays filed for rollback.

// composeProjectName is the docker compose project every release of
// one app runs under: the service names and DNS aliases are
// stable across releases.
func composeProjectName(app domain.App) string {
	return "mymo-" + app.Name
}

// composeStateDir is where a compose app's release files are
// filed on the box.
func composeStateDir(app domain.App) string {
	return path.Join(baseline.StateDir, "compose", app.Name)
}

// releaseComposeFiles are the pair a release files on the box: the
// operator's file and mymo's override for that exact release.
func releaseComposeFiles(app domain.App, id int) (dir, user, mine string) {
	dir = composeStateDir(app)
	return dir,
		fmt.Sprintf("%s/r%d.yaml", dir, id),
		fmt.Sprintf("%s/r%d.mymo.yaml", dir, id)
}

// renderOverride draws mymo's override for one release. The pin
// exists only once docker has assigned an address — the first
// deploy up-asks-records, and rewrites the file with the pin so
// the release's configuration tells the whole truth.
func renderOverride(app domain.App, cf ComposeFile, id int) string {
	var b strings.Builder
	b.WriteString("# mymo override for r")
	fmt.Fprintf(&b, "%d of %s — managed by mymo; edits are overwritten\n", id, app.Name)
	b.WriteString("services:\n")
	for _, svc := range cf.Services {
		b.WriteString("  " + svc + ":\n")
		if cf.Builds[svc] {
			// each built service gets its own release-tagged image,
			// so a rollback runs the release's own build, not
			// whatever a later build left in the stable tag
			fmt.Fprintf(&b, "    image: mymo/%s-%s:r%d\n", app.Name, svc, id)
		}
		if svc == app.Service {
			b.WriteString("    networks:\n")
			b.WriteString("      " + NetworkName + ":\n")
			if app.NetworkIP != "" {
				b.WriteString("        ipv4_address: " + app.NetworkIP + "\n")
			}
		}
	}
	b.WriteString("networks:\n")
	b.WriteString("  " + NetworkName + ":\n")
	b.WriteString("    name: " + NetworkName + "\n")
	b.WriteString("    external: true\n")
	return b.String()
}

// writeRemoteFile writes one small text file on the node through a
// base64 pipe: the content never touches a shell's idea of
// quoting, and the compose files are the only payloads small
// enough to travel this way — contexts go as tarballs.
func writeRemoteFile(ctx context.Context, r Runner, o Options, path, content string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	out, code, err := runW(ctx, r, o, "sh", "-c",
		fmt.Sprintf("printf %%s %s | base64 -d > %s", b64, shquote(path)))
	if err != nil || code != 0 {
		return fmt.Errorf("writing %s failed (%d): %s", path, code, reasonLine(out))
	}
	return nil
}

// shquote quotes one word for the shell the runner uses.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// composeUpArgv assembles the up command for a release's file pair.
func composeUpArgv(project, userFile, mineFile string) []string {
	return []string{"docker", "compose",
		"-p", project,
		"-f", userFile,
		"-f", mineFile,
		"up", "-d", "--wait", "--remove-orphans", "--quiet-pull",
	}
}

// probeHealthService verifies the exposed service's declared
// health: a sibling container on mymo's network dials the service
// by its compose name, the way the network itself resolves it.
func probeHealthService(ctx context.Context, r Runner, o Options, app domain.App, service string) (string, error) {
	if app.Health == "" {
		return "no health endpoint declared — the services running is all mymo can vouch for", nil
	}
	url := fmt.Sprintf("http://%s:%d%s", service, app.Port, app.Health)
	o.report("health gate: asking " + app.Health + " to answer")
	for attempt := 1; attempt <= o.HealthAttempts; attempt++ {
		if out, code, err := runW(ctx, r, o, "docker", "run", "--rm", "--network", NetworkName,
			HealthImage, "wget", "-q", "-T", "2", "-O", "/dev/null", url); err == nil && code == 0 {
			return app.Health + " answers on the network", nil
		} else if attempt == o.HealthAttempts {
			return "", fmt.Errorf("%s never answered (last attempt: %s)", url, reasonLine(out))
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%s was never answered: %v", url, ctx.Err())
		}
	}
	return "", fmt.Errorf("%s never answered", url)
}

// serviceContainer asks compose for the exposed service's container
// id, then docker for that container's address on mymo's network.
func serviceContainerIP(ctx context.Context, r Runner, o Options, project, service string) (string, error) {
	out, code, err := runW(ctx, r, o, "docker", "compose", "-p", project, "ps", "-q", service)
	if err != nil || code != 0 || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the service %s did not answer compose's ps: %s", service, reasonLine(out))
	}
	cid := strings.TrimSpace(strings.Split(out, "\n")[0])
	out, code, err = runW(ctx, r, o, "docker", "inspect", "--format", inspectIPTemplate(), cid)
	if err != nil || code != 0 {
		return "", fmt.Errorf("could not find %s's address on the network: %s", service, reasonLine(out))
	}
	ip := strings.TrimSpace(out)
	if ip == "" {
		return "", fmt.Errorf("%s has no address on %s — the override did not attach it", service, NetworkName)
	}
	return ip, nil
}

// serviceRunning verifies the exposed service carries the state
// compose reports for it.
func serviceRunning(ctx context.Context, r Runner, o Options, project, service string) (string, error) {
	out, code, err := runW(ctx, r, o, "docker", "compose", "-p", project,
		"ps", "--status", "running", "--format", "{{.Service}}", service)
	if err != nil || code != 0 {
		return "", fmt.Errorf("compose did not answer for %s: %s", service, reasonLine(out))
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == service {
			return "running", nil
		}
	}
	return "", fmt.Errorf("%s is not running after activation", service)
}

// deployCompose runs the compose branch of the deploy lifecycle.
// It shares Deploy's steps and failure record — the differences
// are the artifact (files and a context instead of one image) and
// the retirement (up replaces in place; nothing is stopped).
func deployCompose(ctx context.Context, r Runner, app domain.App, o Options, res *Result, id int, step func(control, title string) *StepResult) Result {
	container := composeProjectName(app)
	fail := func(step *StepResult, note string, control string) Result {
		step.State, step.Note = StepFailed, note
		res.OK, res.FailedAt = false, control
		res.App.Releases = append(res.App.Releases, domain.Release{
			ID: id, Digest: ConfigDigest(app.Compose), Source: sourceLine(app, o), Container: container,
			Health: "failed: " + note, State: domain.ReleaseFailed, DeployedAt: o.Now().UTC(),
		})
		res.App.Releases = capReleases(res.App.Releases)
		return *res
	}

	// 2 — the artifact: the vetted file, filed as this release
	cf, perr := ParseCompose(app.Compose)
	if perr != nil {
		return fail(step("file", "compose file"), perr.Error(), "file")
	}
	fileStep := step("file", fmt.Sprintf("compose · %d services", len(cf.Services)))
	if app.Service == "" || !serviceKnown(cf, app.Service) {
		return fail(fileStep, fmt.Sprintf("the exposed service %q is not one this file declares", app.Service), "file")
	}
	dir, userFile, mineFile := releaseComposeFiles(app, id)
	if out, code, err := runW(ctx, r, o, "mkdir", "-p", dir); err != nil || code != 0 {
		return fail(fileStep, fmt.Sprintf("preparing %s failed (%d): %s", dir, code, reasonLine(out)), "file")
	}
	override := renderOverride(app, cf, id)
	if werr := writeRemoteFile(ctx, r, o, userFile, app.Compose); werr != nil {
		return fail(fileStep, werr.Error(), "file")
	}
	if werr := writeRemoteFile(ctx, r, o, mineFile, override); werr != nil {
		return fail(fileStep, werr.Error(), "file")
	}
	fileStep.State, fileStep.Note = StepDone,
		fmt.Sprintf("r%d filed — the operator's file and mymo's override, both on the box", id)

	// builds read their contexts from beside the files; image
	// services need none
	workUser, workMine := userFile, mineFile
	if len(cf.Builds) > 0 {
		ctxStep := step("context", "build context")
		if o.Copier == nil || o.BuildContext == "" {
			return fail(ctxStep, "the compose build needs a context transfer, which is not available", "context")
		}
		dest := fmt.Sprintf("/tmp/mymo-build/%s-r%d", app.Name, id)
		if out, code, err := runW(ctx, r, o, "mkdir", "-p", dest); err != nil || code != 0 {
			return fail(ctxStep, fmt.Sprintf("preparing the build directory failed (%d): %s", code, reasonLine(out)), "context")
		}
		tarball, terr := archiveBuildContext(o.BuildContext)
		if terr != nil {
			return fail(ctxStep, "packing the build context failed: "+terr.Error(), "context")
		}
		o.report("transferring the build context to the node")
		if cerr := o.Copier.Copy(ctx, dest, tarball); cerr != nil {
			return fail(ctxStep, "transferring the build context failed: "+cerr.Error(), "context")
		}
		// the release's pair works from the context: the recorded
		// file over whatever the project carried, so the deploy is
		// the record, not the directory
		workUser = path.Join(dest, "mymo-release.yaml")
		workMine = path.Join(dest, "mymo-release.mymo.yaml")
		if werr := writeRemoteFile(ctx, r, o, workUser, app.Compose); werr != nil {
			return fail(ctxStep, werr.Error(), "context")
		}
		if werr := writeRemoteFile(ctx, r, o, workMine, override); werr != nil {
			return fail(ctxStep, werr.Error(), "context")
		}
		ctxStep.State, ctxStep.Note = StepDone, "the project is on the node, scratch"
	}

	// 3 — build the release's images, each under its release tag
	if len(cf.Builds) > 0 {
		buildStep := step("build", fmt.Sprintf("compose build · %s", joinMapKeys(cf.Builds)))
		o.report("building the release's images on the node")
		out, code, err := runW(ctx, r, o, "docker", "compose", "-p", container,
			"-f", workUser, "-f", workMine, "build")
		if err != nil || code != 0 {
			return fail(buildStep, fmt.Sprintf("build failed (%d): %s", code, buildFailNote(out)), "build")
		}
		buildStep.State, buildStep.Note = StepDone, "built on the node, each image tagged for this release"
	}

	// 4 — up: compose's own healthchecks gate --wait; the exposed
	// service joins mymo's network, pinned once an address exists
	upStep := step("up", "docker compose up")
	if out, code, err := runW(ctx, r, o, composeUpArgv(container, workUser, workMine)...); err != nil || code != 0 {
		return fail(upStep, fmt.Sprintf("the project did not come up (%d): %s", code, reasonLine(out)), "up")
	}
	upStep.State, upStep.Note = StepDone, container+" is up — no port is published"

	// the first deploy asks docker for an address; every later one
	// pins it, and the file now tells the whole truth for rollback
	if app.NetworkIP == "" {
		ip, ipErr := serviceContainerIP(ctx, r, o, container, app.Service)
		if ipErr != nil {
			return fail(upStep, ipErr.Error(), "up")
		}
		app.NetworkIP = ip
		res.App.NetworkIP = ip
		override = renderOverride(app, cf, id)
		if werr := writeRemoteFile(ctx, r, o, mineFile, override); werr != nil {
			return fail(upStep, werr.Error(), "up")
		}
		if len(cf.Builds) > 0 {
			if werr := writeRemoteFile(ctx, r, o, workMine, override); werr != nil {
				return fail(upStep, werr.Error(), "up")
			}
		}
	}

	// 5 — health: what the app declared, asked of the service by
	// its compose name
	health := step("health", "health check")
	healthNote, hErr := probeHealthService(ctx, r, o, app, app.Service)
	if hErr != nil {
		return fail(health, hErr.Error(), "health")
	}
	health.State, health.Note = StepDone, healthNote

	// 6 — activate: the caddy route targets the pinned address;
	// the previous route survives any refusal
	activate := step("activate", "caddy route")
	if app.Domain == "" {
		activate.State, activate.Note = StepKept, "internal only — there is no route to activate"
	} else {
		swapped, aErr := activateRoute(ctx, r, o, domainApp{Name: app.Name, Domain: app.Domain, Port: app.Port, NetworkIP: app.NetworkIP}, container)
		if aErr != nil {
			return fail(activate, aErr.Error(), "activate")
		}
		activate.State, activate.Note = StepDone, swapped
	}

	// 7 — the previous release was replaced in place by up: its
	// containers are gone with --remove-orphans, its configuration
	// stays filed for rollback. There is nothing to stop, and
	// stopping anything would stop this release.
	prev := step("previous", "previous release")
	if old, ok := app.ActiveRelease(); ok {
		prev.State, prev.Note = StepDone, "the previous release was replaced in place — its configuration is filed for rollback"
		for i := range res.App.Releases {
			if res.App.Releases[i].ID == old.ID {
				res.App.Releases[i].State = domain.ReleaseRetired
			}
		}
	} else {
		prev.State, prev.Note = StepKept, "first release — nothing to retire"
	}

	// 8 — verify: the exposed service runs, healthy, and the
	// public path honest
	verify := step("verify", "verification")
	if _, rErr := serviceRunning(ctx, r, o, container, app.Service); rErr != nil {
		return fail(verify, rErr.Error(), "verify")
	}
	if note, hErr := probeHealthService(ctx, r, o, app, app.Service); hErr != nil {
		return fail(verify, "the health check stopped passing after activation: "+hErr.Error(), "verify")
	} else if app.Health != "" {
		verify.Note = note
	}
	if app.Domain != "" {
		if out, _, err := runW(ctx, r, o, "systemctl", "is-active", "caddy"); err != nil || strings.TrimSpace(out) != "active" {
			return fail(verify, "caddy is not active after the reload", "verify")
		}
		verify.Note = "the release serves; caddy reloaded the route cleanly"
		if pub := probeEdge(ctx, r, o, app); pub != "" {
			verify.Note = pub
		}
	}
	if verify.Note == "" {
		verify.Note = "the services run, healthy inside the network"
	}
	verify.State = StepDone

	res.App.Releases = append(res.App.Releases, domain.Release{
		ID: id, Digest: ConfigDigest(app.Compose), Source: sourceLine(app, o), Container: container,
		Health: healthNote, State: domain.ReleaseActive, DeployedAt: o.Now().UTC(),
	})
	res.App.Releases = capReleases(res.App.Releases)
	res.OK = true
	return *res
}

// rollbackCompose replays a previous release's filed pair: no
// rebuild, no new digest — the old release runs its own images
// under its own configuration, up gates it, the route returns.
func rollbackCompose(ctx context.Context, r Runner, app domain.App, o Options, res *Result, target domain.Release, step func(control, title string) *StepResult, fail func(*StepResult, string, string) Result) Result {
	_, userFile, mineFile := releaseComposeFiles(app, target.ID)
	container := composeProjectName(app)

	start := step("rollback", "release r"+fmt.Sprint(target.ID))
	if out, code, err := runW(ctx, r, o, composeUpArgv(container, userFile, mineFile)...); err != nil || code != 0 {
		return fail(start, fmt.Sprintf("r%d did not come back up (%d): %s", target.ID, code, reasonLine(out)), "rollback")
	}
	healthNote, hErr := probeHealthService(ctx, r, o, app, app.Service)
	if hErr != nil {
		return fail(start, "the rollback target failed its health check: "+hErr.Error(), "rollback")
	}
	if _, rErr := serviceRunning(ctx, r, o, container, app.Service); rErr != nil {
		return fail(start, rErr.Error(), "rollback")
	}
	start.State, start.Note = StepDone, "r"+fmt.Sprint(target.ID)+" runs again — "+healthNote

	if app.Domain != "" {
		route := step("rollback", "caddy route")
		ip, ipErr := serviceContainerIP(ctx, r, o, container, app.Service)
		if ipErr != nil {
			return fail(route, ipErr.Error(), "route")
		}
		res.App.NetworkIP = ip
		if swapped, err := activateRoute(ctx, r, o, domainApp{Name: app.Name, Domain: app.Domain, Port: app.Port, NetworkIP: ip}, container); err != nil {
			return fail(route, err.Error(), "route")
		} else {
			route.State, route.Note = StepDone, swapped
		}
	}

	for i := range res.App.Releases {
		if res.App.Releases[i].ID == target.ID {
			res.App.Releases[i].State = domain.ReleaseActive
			res.App.Releases[i].Health = "rolled back at " + o.Now().UTC().Format("2006-01-02T15:04:05Z")
		} else if res.App.Releases[i].State == domain.ReleaseActive {
			res.App.Releases[i].State = domain.ReleaseRetired
		}
	}
	res.OK = true
	return *res
}

func serviceKnown(cf ComposeFile, name string) bool {
	for _, s := range cf.Services {
		if s == name {
			return true
		}
	}
	return false
}

func joinMapKeys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	// a stable order keeps reports and tests honest
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return strings.Join(out, " ")
}
