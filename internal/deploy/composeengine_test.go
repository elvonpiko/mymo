package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/domain"
)

// composeApp is the canonical compose application: an api that
// builds from the project, a database from a registry, the api
// exposed to caddy, health declared.
func composeApp() domain.App {
	return domain.App{
		Name:    "web",
		Node:    "box-1",
		Type:    domain.SourceCompose,
		Service: "api",
		Port:    8080,
		Domain:  "web.example.com",
		Health:  "/healthz",
		Compose: `
services:
  api:
    build: .
    environment:
      - DATABASE_URL=postgres://db:5432/app
    depends_on:
      - db
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data

volumes:
  dbdata:
`,
	}
}

// composeRunner answers the node the way a live one answers a
// compose deploy: the project's services run, the exposed one has
// an address, caddy serves.
func composeRunner() *fakeRunner {
	r := nodeRunner()
	r.codes["docker network inspect mymo-net"] = 0 // the network exists
	r.responses["docker compose -p mymo-web ps -q api"] = "deadbeef01\n"
	r.responses["docker inspect --format {{(index .NetworkSettings.Networks \"mymo-net\").IPAddress}} deadbeef01"] = "172.20.0.21\n"
	r.responses["docker compose -p mymo-web ps --status running --format {{.Service}} api"] = "api\n"
	r.responses["docker run --rm --network mymo-net busybox:1.36 wget -q -T 2 -O /dev/null http://api:8080/healthz"] = ""
	edgeAnswers(r, "193.93.169.48", true)
	return r
}

func composeOpts(t *testing.T, copier *fakeCopier) Options {
	o := goodOpts()
	if copier != nil {
		o.Copier = copier
		o.BuildContext = t.TempDir() // the context is a real directory, tarred for the transfer
	}
	return o
}

// the lifecycle of a compose release: the file is filed, the
// context travels, the release builds under its own tags, up gates
// on --wait, the exposed service is pinned to one address, the
// route swaps, and the record names the whole application
func TestComposeDeployRunsTheLifecycle(t *testing.T) {
	app := composeApp()
	r := composeRunner()
	copier := &fakeCopier{}

	res := Deploy(context.Background(), r, app, composeOpts(t, copier))
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}

	// the order is the compose lifecycle's order
	last := -1
	for _, want := range []string{
		"docker network inspect mymo-net",
		"mkdir -p /var/lib/mymo/compose/web",
		"docker compose -p mymo-web -f /tmp/mymo-build/web-r1/mymo-release.yaml -f /tmp/mymo-build/web-r1/mymo-release.mymo.yaml build",
		"docker compose -p mymo-web -f /tmp/mymo-build/web-r1/mymo-release.yaml -f /tmp/mymo-build/web-r1/mymo-release.mymo.yaml up -d --wait --remove-orphans --quiet-pull",
		"docker compose -p mymo-web ps -q api",
		"docker run --rm --network mymo-net busybox:1.36 wget -q -T 2 -O /dev/null http://api:8080/healthz",
		"caddy validate --config /etc/caddy/Caddyfile",
		"systemctl reload caddy",
		"docker compose -p mymo-web ps --status running --format {{.Service}} api",
	} {
		at := strings.Index(r.callList(), want)
		if at < 0 {
			t.Fatalf("the deploy never ran %q:\n%s", want, r.callList())
		}
		if at < last {
			t.Errorf("%q ran out of order:\n%s", want, r.callList())
		}
		last = at
	}

	// the single-container staging never happens — up is the stage
	if r.called("docker run -d") {
		t.Error("compose must not run a bare docker run")
	}
	if r.called("docker stop") {
		t.Error("compose replaces in place — nothing is stopped")
	}

	// mymo's override carries the model: the network, the pin, the
	// release's own image tags
	if !r.wrote("external: true") {
		t.Error("the override must name mymo's network external")
	}
	if !r.wrote("ipv4_address: 172.20.0.21") {
		t.Error("the pin must be written into the override once the address is known")
	}
	if !r.wrote("image: mymo/web-api:r1") {
		t.Error("each built service must carry its release's image tag")
	}
	if !r.wrote("postgres:16") {
		t.Error("the operator's file must be filed verbatim")
	}

	// the release record: identity is the configuration, the
	// container is the project, the source names the services
	rel := res.App.Releases[len(res.App.Releases)-1]
	if rel.Container != "mymo-web" {
		t.Errorf("container = %q, want the compose project", rel.Container)
	}
	if rel.Digest != ConfigDigest(app.Compose) {
		t.Errorf("digest = %q, want the config digest", rel.Digest)
	}
	if !strings.Contains(rel.Source, "compose · 2 services: api, db") {
		t.Errorf("source = %q, want the services named", rel.Source)
	}
	if res.App.NetworkIP != "172.20.0.21" {
		t.Errorf("the pinned address = %q, want 172.20.0.21", res.App.NetworkIP)
	}
	if copier.dest == "" {
		t.Error("the context must travel to the node")
	}
}

// the second deploy pins the address before up: the route's target
// cannot move, and the override says so from the start
func TestComposeDeployPinsFromTheSecondDeploy(t *testing.T) {
	app := composeApp()
	app.NetworkIP = "172.20.0.21"
	// r1 already active; r2 pins
	app.Releases = []domain.Release{{ID: 1, State: domain.ReleaseActive, Container: "mymo-web", Digest: ConfigDigest("first")}}
	r := composeRunner()

	res := Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	if !r.wrote("ipv4_address: 172.20.0.21") {
		t.Error("the second deploy must write the pin into the override")
	}
	rel := res.App.Releases[len(res.App.Releases)-1]
	if rel.ID != 2 {
		t.Errorf("release id = %d, want 2", rel.ID)
	}
	if res.App.Releases[0].State != domain.ReleaseRetired {
		t.Errorf("the previous release must retire in place, got %q", res.App.Releases[0].State)
	}
}

// the engine trusts nothing it did not check itself: the recorded
// file is re-vetted, and a model breaker refuses the deploy before
// anything touches the box
func TestComposeDeployReVetsTheRecord(t *testing.T) {
	app := composeApp()
	app.Compose = strings.Replace(app.Compose, "    build: .", "    ports:\n      - \"8080:80\"", 1)
	r := composeRunner()

	res := Deploy(context.Background(), r, app, composeOpts(t, nil))
	if res.OK {
		t.Fatal("a published port must refuse the deploy")
	}
	if res.FailedAt != "file" {
		t.Errorf("failure = %s, want the file step", res.FailedAt)
	}
	if !r.called("docker network inspect mymo-net") {
		t.Error("the network is the first step, shared")
	}
	if r.called("docker compose") {
		t.Error("nothing may touch the project after the refusal")
	}
	rel := res.App.Releases[len(res.App.Releases)-1]
	if rel.State != domain.ReleaseFailed || !strings.Contains(rel.Health, "published ports") {
		t.Errorf("the failed release must record the refusal, got %q/%q", rel.State, rel.Health)
	}
}

// an up that fails leaves the route alone and files the failure
// with its reason
func TestComposeDeployFailsWhenUpFails(t *testing.T) {
	app := composeApp()
	r := composeRunner()
	up := "docker compose -p mymo-web -f /tmp/mymo-build/web-r1/mymo-release.yaml -f /tmp/mymo-build/web-r1/mymo-release.mymo.yaml up -d --wait --remove-orphans --quiet-pull"
	r.codes[up] = 1
	r.responses[up] = "Error response from daemon: driver failed programming external connectivity"

	res := Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if res.OK {
		t.Fatal("up failing must fail the deploy")
	}
	if res.FailedAt != "up" {
		t.Errorf("failure = %s, want up", res.FailedAt)
	}
	if r.called("caddy validate") {
		t.Error("the route must never learn about a failed release")
	}
	rel := res.App.Releases[len(res.App.Releases)-1]
	if !strings.Contains(rel.Health, "did not come up") {
		t.Errorf("the failure record = %q", rel.Health)
	}
}

// internal compose apps have no route to activate — the step says
// so, and caddy is never asked anything
func TestComposeInternalAppSkipsCaddy(t *testing.T) {
	app := composeApp()
	app.Domain = ""
	r := composeRunner()

	res := Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	if r.called("caddy") || r.called("systemctl") {
		t.Error("an internal app must not touch caddy")
	}
	internal := false
	for _, s := range res.Steps {
		if s.Control == "activate" && strings.Contains(s.Note, "internal only") {
			internal = true
		}
	}
	if !internal {
		t.Error("the activate step must say internal only")
	}
}

// rollback replays the filed pair: the old release's files, its
// own images, no rebuild — up gates it and the route returns
func TestComposeRollbackReplaysTheFiledPair(t *testing.T) {
	app := composeApp()
	app.NetworkIP = "172.20.0.21"
	app.Releases = []domain.Release{
		{ID: 1, State: domain.ReleaseRetired, Container: "mymo-web", Digest: "sha256:old"},
		{ID: 2, State: domain.ReleaseActive, Container: "mymo-web", Digest: ConfigDigest(app.Compose)},
	}
	r := composeRunner()

	res := Rollback(context.Background(), r, app, composeOpts(t, nil))
	if !res.OK {
		t.Fatalf("rollback failed:\n%s", r.callList())
	}
	if !r.called("docker compose -p mymo-web -f /var/lib/mymo/compose/web/r1.yaml -f /var/lib/mymo/compose/web/r1.mymo.yaml up -d --wait --remove-orphans --quiet-pull") {
		t.Errorf("rollback must replay r1's filed pair:\n%s", r.callList())
	}
	if r.called("build") && !r.called("up -d --wait") {
		t.Error("rollback must never rebuild")
	}
	if res.App.Releases[0].State != domain.ReleaseActive {
		t.Error("r1 must be active again")
	}
	if res.App.Releases[1].State != domain.ReleaseRetired {
		t.Error("r2 must retire")
	}
	if !strings.Contains(res.App.Releases[0].Health, "rolled back") {
		t.Errorf("r1's health note = %q", res.App.Releases[0].Health)
	}
}

// the engine may be handed a compose app with no builds: no
// context travels, the files in the state dir are the pair, and
// the deploy needs no copier at all
func TestComposeImageOnlyDeploysWithoutContext(t *testing.T) {
	app := composeApp()
	app.Compose = `
services:
  api:
    image: ghcr.io/x/api:1.2.3
`
	r := composeRunner()

	res := Deploy(context.Background(), r, app, composeOpts(t, nil))
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	if r.called("mymo-build") {
		t.Error("an image-only compose app must not transfer a context")
	}
	if !r.called("docker compose -p mymo-web -f /var/lib/mymo/compose/web/r1.yaml -f /var/lib/mymo/compose/web/r1.mymo.yaml up -d --wait --remove-orphans --quiet-pull") {
		t.Errorf("the state-dir pair must be the up pair:\n%s", r.callList())
	}
}

// the exposed service must be one the file declares — a route to
// a service that does not exist is a guess mymo refuses to make
func TestComposeDeployRefusesUnknownExposedService(t *testing.T) {
	app := composeApp()
	app.Service = "ghost"
	r := composeRunner()

	res := Deploy(context.Background(), r, app, composeOpts(t, nil))
	if res.OK || res.FailedAt != "file" {
		t.Fatalf("an unknown exposed service must refuse at the file step, got ok=%v at=%s", res.OK, res.FailedAt)
	}
	if !strings.Contains(res.App.Releases[len(res.App.Releases)-1].Health, `"ghost"`) {
		t.Errorf("the refusal must name the service")
	}
}

// A build that fails because the node's disk is full names the
// disk and its fix — the technical reason rides behind the
// ownership, not instead of it
func TestBuildFailureNamesTheFullDisk(t *testing.T) {
	app := composeApp()
	r := composeRunner()
	build := "docker compose -p mymo-web -f /tmp/mymo-build/web-r1/mymo-release.yaml -f /tmp/mymo-build/web-r1/mymo-release.mymo.yaml build"
	r.codes[build] = 1
	r.responses[build] = "#10 writing image sha256:abc\ncopying files: failed to copy files: write /var/lib/docker/tmp/...: no space left on device"

	res := Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if res.OK {
		t.Fatal("a disk-full build must fail the deploy")
	}
	if res.FailedAt != "build" {
		t.Errorf("failure = %s, want build", res.FailedAt)
	}
	rel := res.App.Releases[len(res.App.Releases)-1]
	if !strings.Contains(rel.Health, "the node's disk is full") {
		t.Errorf("the refusal should name the disk and its fix: %q", rel.Health)
	}
	if !strings.Contains(rel.Health, "no space left on device") {
		t.Errorf("the technical reason should ride along: %q", rel.Health)
	}
	if r.called("caddy validate") {
		t.Error("the route must never learn about a failed release")
	}
}

// An interrupted compose deploy is recovered by the next attempt:
// the attempt that never recorded a release hands the same id
// back, the pair is rewritten, and up recreates what it finds —
// compose's own recovery, not a new one mymo invents
func TestComposeDeployRecoversTheInterruptedAttempt(t *testing.T) {
	app := composeApp()
	r := composeRunner()
	up := "docker compose -p mymo-web -f /tmp/mymo-build/web-r1/mymo-release.yaml -f /tmp/mymo-build/web-r1/mymo-release.mymo.yaml up -d --wait --remove-orphans --quiet-pull"
	r.codes[up] = 1
	r.responses[up] = "Error response from daemon: container command interrupted"

	res := Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if res.OK {
		t.Fatal("the first attempt must fail here")
	}
	if res.App.Releases[len(res.App.Releases)-1].State != domain.ReleaseFailed {
		t.Fatal("the interrupted attempt files its failure")
	}

	// the operator runs again: the failed attempt is history the
	// next deploy reads from the record, exactly as the store
	// carries it
	app = res.App
	r.codes[up] = 0
	res = Deploy(context.Background(), r, app, composeOpts(t, &fakeCopier{}))
	if !res.OK {
		t.Fatalf("the recovery failed at %s:\n%s", res.FailedAt, r.callList())
	}
	rel := res.App.Releases[len(res.App.Releases)-1]
	if rel.ID != 2 {
		t.Errorf("the recovered deploy should be r2, got r%d — a failed attempt consumes its id", rel.ID)
	}
	if rel.State != domain.ReleaseActive {
		t.Errorf("the recovered release should be active, says %q", rel.State)
	}
	if !r.wrote("image: mymo/web-api:r2") {
		t.Error("the recovered deploy builds its own release tag")
	}
}
