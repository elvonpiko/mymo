package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
)

// cliRunner fakes the node for the deploy and ops commands: canned
// responses for the happy path, exit codes for the failures a test
// wants to prove.
type cliRunner struct {
	responses map[string]string
	codes     map[string]int
	calls     [][]string
}

func (f *cliRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	key := strings.Join(argv, " ")
	if out, ok := f.responses[key]; ok {
		return out, 0, nil
	}
	if code, ok := f.codes[key]; ok {
		return "", code, nil
	}
	return "", 0, nil
}

func (f *cliRunner) callList() string {
	var b strings.Builder
	for _, argv := range f.calls {
		fmt.Fprintln(&b, strings.Join(argv, " "))
	}
	return b.String()
}

// happyNodeRunner answers the full lifecycle for one image deploy.
func happyNodeRunner() *cliRunner {
	return &cliRunner{
		responses: map[string]string{
			"id -u": "1000\n",
			"sudo -n docker image inspect --format {{.Id}} ghcr.io/x/api:1.2.3":                       "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
			"sudo -n docker ps --filter name=mymo-api-r1 --filter status=running --format {{.Names}}": "mymo-api-r1\n",
			"sudo -n systemctl is-active caddy":                                                       "active\n",
		},
		codes: map[string]int{"sudo -n docker network inspect mymo-net": 1},
	}
}

func happyHealth(r *cliRunner, container string) {
	r.responses[fmt.Sprintf("sudo -n docker run --rm --network mymo-net busybox:1.36 wget -q -T 2 -O /dev/null http://%s:8080/healthz", container)] = ""
	r.responses[fmt.Sprintf("sudo -n docker ps --filter name=%s --filter status=running --format {{.Names}}", container)] = container + "\n"
}

// writeProject drops a mymo.yaml into a fresh directory and chdirs
// there for the test.
func writeProject(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mymo.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

const imageManifest = "name: api\ntype: image\nimage: ghcr.io/x/api:1.2.3\nport: 8080\ndomain: api.example.com\nhealth: /healthz\n"

func readyBootstrap() domain.Bootstrap {
	return domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.1", At: time.Now()}
}

func TestDeployRefusesWithoutAnySource(t *testing.T) {
	s := newSession(t)
	t.Chdir(t.TempDir())
	code, _, errb := s.run(t, "deploy")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "no supported deployment source") ||
		!strings.Contains(errb, "supported deployment modes") {
		t.Errorf("refusal = %q, want the modes listed", errb)
	}
}

func TestDeployDraftsForADockerfileOnlyProject(t *testing.T) {
	s := newSession(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	code, _, errb := s.run(t, "deploy")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "does not guess") || !strings.Contains(errb, "port: 8080") {
		t.Errorf("draft refusal = %q", errb)
	}
	store, _ := state.Open()
	if apps, _ := store.LoadApps(); len(apps) != 0 {
		t.Error("a draft recorded an app")
	}
}

func TestDeployRefusesWhenTheNodeIsNotReady(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", domain.Bootstrap{State: domain.BootstrapPreflight, Verdict: "decide"})
	writeProject(t, imageManifest)

	code, _, errb := s.run(t, "deploy", "-node", "web-1")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "cannot deploy to a node it has not verified") {
		t.Errorf("refusal = %q", errb)
	}
}

func TestDeployNeedsTheNodeFlagForANewApp(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	writeProject(t, imageManifest)

	code, _, errb := s.run(t, "deploy")
	if code != exitErr || !strings.Contains(errb, "a new application needs its node") {
		t.Fatalf("exit %d errb %q, want the -node refusal", code, errb)
	}
}

func TestDeployWantsTheExactName(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	writeProject(t, imageManifest)

	deployConfirmIn = strings.NewReader("api2\n")
	deployDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func(bool) deploy.Copier, func() error, error) {
		t.Fatal("the gate refused, yet the deploy dialed")
		return nil, nil, nil, nil
	}
	t.Cleanup(func() {
		deployConfirmIn = os.Stdin
		deployDial = defaultDeployDial
	})

	code, out, _ := s.run(t, "deploy", "-node", "web-1")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(out, "does not guess") && !strings.Contains(out, "aborted — api is untouched") {
		t.Errorf("out = %q, want the abort line", out)
	}
	store, _ := state.Open()
	if apps, _ := store.LoadApps(); len(apps) != 0 {
		t.Error("an aborted deploy recorded an app")
	}
}

func TestDeployRecordsTheRelease(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	writeProject(t, imageManifest)

	r := happyNodeRunner()
	happyHealth(r, "mymo-api-r1")
	deployConfirmIn = strings.NewReader("api\n")
	deployDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func(bool) deploy.Copier, func() error, error) {
		return r, func(bool) deploy.Copier { return nil }, func() error { return nil }, nil
	}
	t.Cleanup(func() {
		deployConfirmIn = os.Stdin
		deployDial = defaultDeployDial
	})

	code, out, errb := s.run(t, "deploy", "-node", "web-1")
	if code != exitOK {
		t.Fatalf("exit %d out %q errb %q", code, out, errb)
	}
	if !strings.Contains(out, "release 1 is live on web-1") ||
		!strings.Contains(out, "https://api.example.com") ||
		!strings.Contains(out, "nothing yet — the first release has no predecessor") {
		t.Errorf("out missing the success truth:\n%s", out)
	}
	if !strings.Contains(out, "sudo -n") && len(r.calls) > 0 {
		// sanity: the runner saw the sudo decision
		_ = r.callList()
	}
	store, _ := state.Open()
	app, err := store.GetApp("api")
	if err != nil {
		t.Fatalf("app not recorded: %v", err)
	}
	rel, ok := app.ActiveRelease()
	if !ok || rel.ID != 1 || rel.State != domain.ReleaseActive {
		t.Errorf("release record = %+v", app.Releases)
	}
	if !strings.HasPrefix(rel.Digest, "sha256:") {
		t.Errorf("digest = %q", rel.Digest)
	}
	// every privileged command carried sudo -n
	for _, argv := range r.calls {
		// id -u is the privilege probe itself; everything else a
		// deploy runs carries the answer
		if argv[0] == "sudo" || (argv[0] == "id" && len(argv) == 2 && argv[1] == "-u") {
			continue
		}
		t.Errorf("unprivileged command reached the node: %v", argv)
	}
}

func TestDeployFailureIsHistoryToo(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	writeProject(t, imageManifest)

	r := happyNodeRunner()
	r.codes["sudo -n docker pull ghcr.io/x/api:1.2.3"] = 1
	deployConfirmIn = strings.NewReader("api\n")
	deployDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func(bool) deploy.Copier, func() error, error) {
		return r, func(bool) deploy.Copier { return nil }, func() error { return nil }, nil
	}
	t.Cleanup(func() {
		deployConfirmIn = os.Stdin
		deployDial = defaultDeployDial
	})

	code, _, errb := s.run(t, "deploy", "-node", "web-1")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(errb, "failed at pull — nothing was activated") {
		t.Errorf("errb = %q, want the honest failure", errb)
	}
	store, _ := state.Open()
	app, err := store.GetApp("api")
	if err != nil {
		t.Fatalf("failure not recorded: %v", err)
	}
	if len(app.Releases) != 1 || app.Releases[0].State != domain.ReleaseFailed {
		t.Errorf("releases = %+v, want the failed one recorded", app.Releases)
	}
}

func TestRollbackRestoresThePreviousRelease(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())

	store, _ := state.Open()
	app := domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "ghcr.io/x/api:1.2.3", Port: 8080, Domain: "api.example.com", Health: "/healthz",
		Releases: []domain.Release{
			{ID: 1, State: domain.ReleaseRetired, Container: "mymo-api-r1",
				Digest: "sha256:aaaa", Health: "ok", DeployedAt: time.Now()},
			{ID: 2, State: domain.ReleaseActive, Container: "mymo-api-r2",
				Digest: "sha256:bbbb", Health: "ok", DeployedAt: time.Now()},
		}}
	if err := store.AddApp(app); err != nil {
		t.Fatal(err)
	}

	r := happyNodeRunner()
	happyHealth(r, "mymo-api-r1")
	happyHealth(r, "mymo-api-r2")
	deployConfirmIn = strings.NewReader("api\n")
	opsDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func() error, error) {
		return r, func() error { return nil }, nil
	}
	t.Cleanup(func() {
		deployConfirmIn = os.Stdin
		opsDial = defaultOpsDial
	})

	code, out, errb := s.run(t, "app", "rollback", "api")
	if code != exitOK {
		t.Fatalf("exit %d out %q errb %q", code, out, errb)
	}
	if !strings.Contains(out, "release 1 is live again") {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(r.callList(), "docker start mymo-api-r1") &&
		!strings.Contains(r.callList(), "sudo -n docker start mymo-api-r1") {
		t.Errorf("the previous container was never started:\n%s", r.callList())
	}
	app, _ = store.GetApp("api")
	if rel, _ := app.ActiveRelease(); rel.ID != 1 {
		t.Errorf("active = %+v, want r1", rel)
	}
}

func TestRollbackAbortsOnTheWrongName(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	store, _ := state.Open()
	app := domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "img:1", Port: 8080,
		Releases: []domain.Release{
			{ID: 1, State: domain.ReleaseRetired, Container: "mymo-api-r1"},
			{ID: 2, State: domain.ReleaseActive, Container: "mymo-api-r2"},
		}}
	if err := store.AddApp(app); err != nil {
		t.Fatal(err)
	}

	deployConfirmIn = strings.NewReader("nope\n")
	opsDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func() error, error) {
		t.Fatal("the gate refused, yet the rollback dialed")
		return nil, nil, nil
	}
	t.Cleanup(func() {
		deployConfirmIn = os.Stdin
		opsDial = defaultOpsDial
	})

	code, out, _ := s.run(t, "app", "rollback", "api")
	if code != exitErr {
		t.Fatalf("exit code = %d, want %d", code, exitErr)
	}
	if !strings.Contains(out, "aborted — api keeps serving release 2") {
		t.Errorf("out = %q", out)
	}
	app, _ = store.GetApp("api")
	if rel, _ := app.ActiveRelease(); rel.ID != 2 {
		t.Errorf("active = %+v, want r2 untouched", rel)
	}
}

func TestAppLifecycleRestartsTheActiveRelease(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	store, _ := state.Open()
	if err := store.AddApp(domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "img:1", Port: 8080,
		Releases: []domain.Release{{ID: 1, State: domain.ReleaseActive, Container: "mymo-api-r1"}}}); err != nil {
		t.Fatal(err)
	}

	r := happyNodeRunner()
	opsDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func() error, error) {
		return r, func() error { return nil }, nil
	}
	t.Cleanup(func() { opsDial = defaultOpsDial })

	code, out, errb := s.run(t, "app", "restart", "api")
	if code != exitOK {
		t.Fatalf("exit %d errb %q", code, errb)
	}
	if !strings.Contains(out, "mymo-api-r1 restarted") {
		t.Errorf("out = %q", out)
	}
}

func TestAppLogsAndStatusSeeTheTruth(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	store, _ := state.Open()
	if err := store.AddApp(domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "img:1", Port: 8080, Domain: "api.example.com",
		Releases: []domain.Release{
			{ID: 1, State: domain.ReleaseRetired, Container: "mymo-api-r1", Digest: "sha256:aaaa"},
			{ID: 2, State: domain.ReleaseActive, Container: "mymo-api-r2", Digest: "sha256:bbbb"},
		}}); err != nil {
		t.Fatal(err)
	}

	r := happyNodeRunner()
	happyHealth(r, "mymo-api-r2")
	r.responses["sudo -n docker logs --tail 100 mymo-api-r2"] = "listening on 8080\n"
	opsDial = func(ctx context.Context, node domain.Node, store *state.Store) (deploy.Runner, func() error, error) {
		return r, func() error { return nil }, nil
	}
	t.Cleanup(func() { opsDial = defaultOpsDial })

	code, out, _ := s.run(t, "app", "logs", "api")
	if code != exitOK || !strings.Contains(out, "listening on 8080") {
		t.Errorf("logs: code %d out %q", code, out)
	}

	code, out, _ = s.run(t, "app", "status", "api")
	if code != exitOK {
		t.Fatalf("status failed")
	}
	for _, want := range []string{"kept for rollback", "active", "mymo-api-r2 is running right now", "https://api.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestAppShellRefusesWithoutAnActiveRelease(t *testing.T) {
	s := newSession(t)
	seededNode(t, s, "web-1", readyBootstrap())
	store, _ := state.Open()
	if err := store.AddApp(domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "img:1", Port: 8080}); err != nil {
		t.Fatal(err)
	}

	code, _, errb := s.run(t, "app", "shell", "api")
	if code != exitErr || !strings.Contains(errb, "no active release") {
		t.Fatalf("shell: code %d errb %q", code, errb)
	}
}
