package deploy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
)

// fakeRunner answers canned responses keyed by the joined argv;
// unknown commands succeed quietly, so each test states only what
// it cares about — including its failures. Guarded writes update a
// small file map so cat answers what was actually written — the
// restore-after-refusal path depends on reading reality back.
type fakeRunner struct {
	responses map[string]string
	codes     map[string]int
	calls     [][]string
	files     map[string]string
	// catErrors answers a cat of a missing file the way the real
	// transport does: the complaint folded into the output, exit 1
	catErrors map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	key := strings.Join(argv, " ")

	// a guarded write lands: decode, remember, succeed
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" && strings.Contains(argv[2], "base64 -d") {
		fields := strings.Fields(argv[2])
		if len(fields) >= 3 {
			if b, err := base64.StdEncoding.DecodeString(fields[2]); err == nil && len(fields) >= 13 {
				// the script ends with: mv <tmp> <path>
				path := fields[len(fields)-1]
				if f.files == nil {
					f.files = map[string]string{}
				}
				f.files[path] = string(b)
			}
		}
		return "", 0, nil
	}

	// docker inspect answers like the real one: exit 0 with the
	// object when it exists, exit 1 when it does not — the orphan
	// detection depends on the difference
	if name == "docker" && len(args) >= 2 && args[0] == "inspect" {
		if out, ok := f.responses[key]; ok {
			return out, 0, nil
		}
		if code, ok := f.codes[key]; ok {
			return "", code, nil
		}
		return "Error: No such object: " + strings.Join(args[1:], " "), 1, nil
	}

	if name == "cat" {
		if f.files != nil {
			if content, ok := f.files[strings.Join(args, " ")]; ok {
				return content, 0, nil
			}
		}
		if miss, ok := f.catErrors[strings.Join(args, " ")]; ok {
			return miss, 1, nil
		}
	}
	// a coded failure carries its output when one is stated — the
	// real transport returns both, and a build's diagnosis lives in
	// the output beside the exit code
	if code, ok := f.codes[key]; ok {
		return f.responses[key], code, nil
	}
	if out, ok := f.responses[key]; ok {
		return out, 0, nil
	}
	return "", 0, nil
}

func (f *fakeRunner) called(needle string) bool {
	for _, argv := range f.calls {
		if strings.Contains(strings.Join(argv, " "), needle) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) callList() string {
	var b strings.Builder
	for _, argv := range f.calls {
		fmt.Fprintln(&b, strings.Join(argv, " "))
	}
	return b.String()
}

// writtenContents decodes what the guarded writes actually put on
// the node — file content travels as base64 inside sh -c.
func (f *fakeRunner) writtenContents() []string {
	var out []string
	for _, argv := range f.calls {
		if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
			continue
		}
		script := argv[2]
		if !strings.Contains(script, "base64 -d") {
			continue
		}
		fields := strings.Fields(script)
		if len(fields) < 3 {
			continue
		}
		if b, err := base64.StdEncoding.DecodeString(fields[2]); err == nil {
			out = append(out, string(b))
		}
	}
	return out
}

func (f *fakeRunner) wrote(content string) bool {
	for _, w := range f.writtenContents() {
		if strings.Contains(w, content) {
			return true
		}
	}
	return false
}

// fakeCopier records that the context traveled, and where.
type fakeCopier struct {
	dest   string
	tarred bool
}

func (c *fakeCopier) Copy(_ context.Context, dest string, tarball io.Reader) error {
	c.dest = dest
	_, err := io.ReadAll(tarball)
	c.tarred = err == nil
	return err
}

func imageApp() domain.App {
	return domain.App{
		Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "ghcr.io/x/api:1.2.3", Port: 8080,
		Domain: "api.example.com", Health: "/healthz",
	}
}

func goodOpts() Options {
	return Options{
		Now:            func() time.Time { return time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC) },
		HealthAttempts: 1, HealthPause: 0,
	}
}

// nodeRunner answers everything a healthy node says to the happy
// path, plus the digests for two release tags.
func nodeRunner() *fakeRunner {
	r := &fakeRunner{responses: map[string]string{
		"docker image inspect --format {{.Id}} ghcr.io/x/api:1.2.3":                            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		"docker image inspect --format {{.Id}} mymo/api:r1":                                    "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		"docker image inspect --format {{.Id}} mymo/api:r2":                                    "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\n",
		"docker image inspect --format {{.Id}} mymo/api:r3":                                    "sha256:1111111111111111111111111111111111111111111111111111111111111111\n",
		"systemctl is-active caddy":                                                            "active\n",
		"docker inspect --format {{.NetworkSettings.Networks.mymo-net.IPAddress}} mymo-api-r1": "172.20.0.11\n",
		"docker inspect --format {{.NetworkSettings.Networks.mymo-net.IPAddress}} mymo-api-r2": "172.20.0.12\n",
		"docker inspect --format {{.NetworkSettings.Networks.mymo-net.IPAddress}} mymo-api-r3": "172.20.0.13\n",
	}, codes: map[string]int{}}
	// the network is absent the first time — the deploy creates it
	r.codes["docker network inspect mymo-net"] = 1
	return r
}

func firstDeployHealth(r *fakeRunner, container string) {
	r.responses[fmt.Sprintf("docker run --rm --network mymo-net busybox:1.36 wget -q -T 2 -O /dev/null http://%s:8080/healthz", container)] = ""
	r.responses[fmt.Sprintf("docker ps --filter name=%s --filter status=running --format {{.Names}}", container)] = container + "\n"
}

func TestImageReleaseRunsTheLifecycle(t *testing.T) {
	app := imageApp()
	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")

	res := Deploy(context.Background(), r, app, goodOpts())
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	// the lifecycle's order is the spec's order
	last := -1
	for _, want := range []string{
		"docker network inspect mymo-net",
		"docker network create mymo-net",
		"docker pull ghcr.io/x/api:1.2.3",
		"docker image inspect --format {{.Id}} ghcr.io/x/api:1.2.3",
		"docker run -d --name mymo-api-r1",
		"caddy validate --config /etc/caddy/Caddyfile",
		"systemctl reload caddy",
		"docker ps --filter name=mymo-api-r1",
		"systemctl is-active caddy",
	} {
		found := -1
		for j, argv := range r.calls {
			if j > last && strings.Contains(strings.Join(argv, " "), want) {
				found = j
				break
			}
		}
		if found < 0 {
			t.Errorf("lifecycle out of order or missing %q:\n%s", want, r.callList())
			continue
		}
		last = found
	}
	if len(res.App.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(res.App.Releases))
	}
	rel := res.App.Releases[0]
	if rel.ID != 1 || rel.State != domain.ReleaseActive || rel.Container != "mymo-api-r1" {
		t.Errorf("release = %+v", rel)
	}
	if rel.Digest != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("digest = %q", rel.Digest)
	}
	if !strings.Contains(rel.Health, "/healthz answers") {
		t.Errorf("health = %q", rel.Health)
	}
	// the route targets the app's pinned address — the host's
	// caddy can dial it, and a restart cannot change it
	if !r.wrote("reverse_proxy 172.20.0.11:8080") {
		t.Errorf("route does not target the pinned address:\n%s", r.callList())
	}
	if res.App.NetworkIP != "172.20.0.11" {
		t.Errorf("the record did not pin the address: %q", res.App.NetworkIP)
	}
}

// The public path is verified from the box itself, and a
// still-issuing certificate is not lied about: the verify step
// says the domain does not answer yet and why — tls issues once
// the name resolves to the box, and caddy keeps retrying.
func TestPublicNotYetIsStatedNotClaimed(t *testing.T) {
	app := imageApp()
	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	// the public probe from the host never connects: the cert is
	// still issuing
	r.codes["docker run --rm --network host busybox:1.36 wget -q -T 3 -O /dev/null --no-check-certificate https://api.example.com"] = 1
	opts := goodOpts()
	res := Deploy(context.Background(), r, app, opts)
	if !res.OK {
		t.Fatalf("a dark public path is not a failed deploy:\n%s", r.callList())
	}
	var note string
	for _, st := range res.Steps {
		if st.Control == "verify" {
			note = st.Note
		}
	}
	if !strings.Contains(note, "does not answer publicly yet") ||
		!strings.Contains(note, "tls is issued once the name resolves to this box") {
		t.Errorf("the verify note must state the not-yet:\n%s", note)
	}
}

// The networking model's invariant: no managed container ever
// publishes a port. The only path in is the network.
func TestPortsAreNeverPublished(t *testing.T) {
	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	Deploy(context.Background(), r, imageApp(), goodOpts())

	// the published-port ban applies to every docker run, whoever
	// asks for it
	for _, argv := range r.calls {
		if argv[0] != "docker" || len(argv) < 2 || argv[1] != "run" {
			continue
		}
		for _, a := range argv {
			if a == "-p" || a == "--publish" || strings.HasPrefix(a, "-p:") || strings.HasPrefix(a, "--publish=") {
				t.Fatalf("a port was published: %v", argv)
			}
		}
	}
	for _, argv := range r.calls {
		// the app container: named, on the network, never published
		if argv[0] == "docker" && len(argv) > 2 && argv[1] == "run" && argv[2] == "-d" {
			var hasNetwork, hasName bool
			for _, a := range argv {
				if a == "--network" {
					hasNetwork = true
				}
				if a == "--name" {
					hasName = true
				}
			}
			if !hasNetwork || !hasName {
				t.Errorf("docker run without network/name discipline: %v", argv)
			}
		}
	}
}

func TestDockerfileBuildsOnTheNode(t *testing.T) {
	app := imageApp()
	app.Type = domain.SourceDockerfile
	app.Image = ""
	app.Dockerfile = "."
	app.Domain = "" // internal only: no caddy anywhere
	app.Health = ""

	r := nodeRunner()
	copier := &fakeCopier{}
	firstDeployHealth(r, "mymo-api-r1")
	opts := goodOpts()
	opts.Copier = copier
	opts.BuildContext = t.TempDir()
	var heard []string
	opts.Progress = func(line string) { heard = append(heard, line) }

	res := Deploy(context.Background(), r, app, opts)
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	// the long steps narrate as they run — a silent stretch reads
	// as a hang on the loading ceremony
	for _, want := range []string{
		"packing the build context",
		"transferring the build context to the node",
		"building mymo/api:r1 on the node",
	} {
		found := false
		for _, line := range heard {
			if strings.Contains(line, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("the deploy never said %q — heard %v", want, heard)
		}
	}
	if !copier.tarred || copier.dest != "/tmp/mymo-build/api-r1" {
		t.Errorf("context transfer = %+v", copier)
	}
	if !r.called("docker build -t mymo/api:r1 /tmp/mymo-build/api-r1") {
		t.Errorf("the build did not run on the node:\n%s", r.callList())
	}
	if !r.called("rm -rf /tmp/mymo-build/api-r1") {
		t.Error("the build scratch dir was left behind")
	}
	if r.called("caddy validate") || r.called("systemctl reload caddy") {
		t.Error("an internal-only deploy touched caddy")
	}
	// the release runs under the built tag, recorded by digest
	rel := res.App.Releases[0]
	if rel.Source != "dockerfile ." || rel.Digest == "" {
		t.Errorf("release = %+v", rel)
	}
	if !r.called("docker run -d --name mymo-api-r1 --network mymo-net --network-alias api --restart unless-stopped mymo/api:r1") {
		t.Errorf("the container did not run the built tag:\n%s", r.callList())
	}
}

func TestHealthFailureStopsTheNewReleaseAndKeepsTheOld(t *testing.T) {
	app := imageApp()
	// r1 is live and serving
	app.Releases = []domain.Release{{
		ID: 1, State: domain.ReleaseActive, Container: "mymo-api-r1",
		Digest: "sha256:aaaa", Health: "ok", DeployedAt: time.Now(),
	}}

	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	// the new release's health never answers
	r.codes["docker run --rm --network mymo-net busybox:1.36 wget -q -T 2 -O /dev/null http://mymo-api-r2:8080/healthz"] = 1

	res := Deploy(context.Background(), r, app, goodOpts())
	if res.OK || res.FailedAt != "health" {
		t.Fatalf("result ok=%v failedAt=%s, want failure at health", res.OK, res.FailedAt)
	}
	if !r.called("docker stop mymo-api-r2") {
		t.Error("the unhealthy release was not stopped")
	}
	if r.called("docker stop mymo-api-r1") {
		t.Error("the current release was touched — it must keep serving")
	}
	if r.called("systemctl reload caddy") {
		t.Error("caddy was reloaded for a failed release")
	}
	// the failure is history, not a silent gap
	if len(res.App.Releases) != 2 || res.App.Releases[1].State != domain.ReleaseFailed {
		t.Errorf("releases = %+v", res.App.Releases)
	}
	if _, ok := res.App.ActiveRelease(); !ok || ok && res.App.Releases[0].ID != 1 {
		t.Error("r1 must remain the active release")
	}
}

func TestCaddyRefusalRestoresTheOldRouteAndNeverReloads(t *testing.T) {
	app := imageApp()
	app.Releases = []domain.Release{{
		ID: 1, State: domain.ReleaseActive, Container: "mymo-api-r1",
		Digest: "sha256:aaaa", Health: "ok", DeployedAt: time.Now(),
	}}

	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	firstDeployHealth(r, "mymo-api-r2")
	r.codes["caddy validate --config /etc/caddy/Caddyfile"] = 1
	// r1's route is live on the node — the refusal must restore it
	r.responses["cat /etc/caddy/mymo/api.caddy"] = renderRoute("api", "api.example.com", "172.20.0.11", 8080)

	res := Deploy(context.Background(), r, app, goodOpts())
	if res.OK || res.FailedAt != "activate" {
		t.Fatalf("result ok=%v failedAt=%s, want failure at activate", res.OK, res.FailedAt)
	}
	if r.called("systemctl reload caddy") {
		t.Error("the reload ran despite an invalid route")
	}
	if !r.called("docker stop mymo-api-r2") {
		t.Error("the unactivatable release keeps running")
	}
	// the old route content was written back: the serving config is
	// the one that was serving
	if !r.wrote("reverse_proxy 172.20.0.11:8080") || !r.wrote("reverse_proxy 172.20.0.12:8080") {
		t.Errorf("route was not swapped and restored:\n%s", r.callList())
	}
	// r1 keeps serving; r2 is a recorded failure
	if act, _ := res.App.ActiveRelease(); act.ID != 1 {
		t.Errorf("active = %+v, want r1", act)
	}
	if res.App.Releases[1].State != domain.ReleaseFailed {
		t.Errorf("r2 = %+v, want failed", res.App.Releases[1])
	}
}

func TestSecondDeployRetiresKeepsAndEventuallyPrunes(t *testing.T) {
	app := imageApp()
	r1 := domain.Release{ID: 1, State: domain.ReleaseActive, Container: "mymo-api-r1",
		Digest: "sha256:aaaa", Health: "ok", DeployedAt: time.Now()}
	app.Releases = []domain.Release{r1}

	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	firstDeployHealth(r, "mymo-api-r2")
	firstDeployHealth(r, "mymo-api-r3")

	res := Deploy(context.Background(), r, app, goodOpts())
	if !res.OK {
		t.Fatalf("second deploy failed:\n%s", r.callList())
	}
	if !r.called("docker stop mymo-api-r1") {
		t.Error("the previous release was not stopped")
	}
	if r.called("docker rm mymo-api-r1") {
		t.Error("the rollback release was pruned too early")
	}
	if act, _ := res.App.ActiveRelease(); act.ID != 2 {
		t.Errorf("active = %+v, want r2", act)
	}
	if res.App.Releases[0].State != domain.ReleaseRetired {
		t.Errorf("r1 = %+v, want retired", res.App.Releases[0])
	}

	// r3: r1 falls outside the rollback window and is pruned
	res2 := Deploy(context.Background(), r, res.App, goodOpts())
	if !res2.OK {
		t.Fatalf("third deploy failed:\n%s", r.callList())
	}
	// every later release pins the address the first one was
	// assigned — that is the point of a pinned address
	if !r.called("--ip 172.20.0.12") {
		t.Errorf("the later deploys did not pin the assigned address:\n%s", r.callList())
	}
	if !r.called("docker rm mymo-api-r1") {
		t.Error("the release outside the rollback window was never pruned")
	}
	if !r.called("docker stop mymo-api-r2") {
		t.Error("the new previous release was not stopped")
	}
	if act, _ := res2.App.ActiveRelease(); act.ID != 3 {
		t.Errorf("active = %+v, want r3", act)
	}
}

// TestFirstDeployWithNoCaddyfile reproduces the fresh-box shape: the
// Caddyfile read fails and the complaint folds into the output with
// exit 1. That text must never land inside the serving config.
func TestFirstDeployWithNoCaddyfile(t *testing.T) {
	app := imageApp()
	r := nodeRunner()
	r.catErrors = map[string]string{
		"/etc/caddy/Caddyfile": "cat: /etc/caddy/Caddyfile: No such file or directory",
	}
	firstDeployHealth(r, "mymo-api-r1")

	res := Deploy(context.Background(), r, app, goodOpts())
	if !res.OK {
		t.Fatalf("deploy failed at %s:\n%s", res.FailedAt, r.callList())
	}
	var caddyfile string
	found := false
	for _, w := range r.writtenContents() {
		if strings.Contains(w, "import mymo/*.caddy") {
			caddyfile, found = w, true
		}
	}
	if !found {
		t.Fatal("the Caddyfile was never wired")
	}
	if strings.Contains(caddyfile, "cat:") {
		t.Errorf("cat's complaint became the serving config:\n%s", caddyfile)
	}
	if !strings.HasPrefix(caddyfile, "import mymo/*.caddy") {
		t.Errorf("the Caddyfile should be only the import for a fresh box:\n%s", caddyfile)
	}
}

func TestRollbackIsDeterministic(t *testing.T) {
	app := imageApp()
	app.Releases = []domain.Release{
		{ID: 1, State: domain.ReleaseRetired, Container: "mymo-api-r1",
			Digest: "sha256:aaaa", Health: "ok", DeployedAt: time.Now()},
		{ID: 2, State: domain.ReleaseActive, Container: "mymo-api-r2",
			Digest: "sha256:bbbb", Health: "ok", DeployedAt: time.Now()},
	}
	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	firstDeployHealth(r, "mymo-api-r2")

	res := Rollback(context.Background(), r, app, goodOpts())
	if !res.OK {
		t.Fatalf("rollback failed at %s:\n%s", res.FailedAt, r.callList())
	}
	if !r.called("docker start mymo-api-r1") {
		t.Error("rollback must start the recorded container, not rebuild")
	}
	if r.called("docker pull") || r.called("docker build") {
		t.Error("rollback rebuilt — it must select a known release")
	}
	if !r.called("docker stop mymo-api-r2") {
		t.Error("the rolled-back release keeps running")
	}
	// the route points back at r1's address
	if !r.wrote("reverse_proxy 172.20.0.11:8080") {
		t.Errorf("route not restored to r1:\n%s", r.callList())
	}
	if res.App.NetworkIP != "172.20.0.11" {
		t.Errorf("the rollback did not keep the pinned address: %q", res.App.NetworkIP)
	}
	if act, _ := res.App.ActiveRelease(); act.ID != 1 {
		t.Errorf("active = %+v, want r1", act)
	}
	if res.App.Releases[1].State != domain.ReleaseRetired {
		t.Errorf("r2 = %+v, want retired", res.App.Releases[1])
	}
}

func TestRollbackRefusesWhenThereIsNoTarget(t *testing.T) {
	app := imageApp()
	app.Releases = []domain.Release{{ID: 1, State: domain.ReleaseFailed, Container: "mymo-api-r1"}}
	res := Rollback(context.Background(), nodeRunner(), app, goodOpts())
	if res.OK || res.FailedAt != "rollback" {
		t.Fatalf("rollback of a failed-only history: %+v", res)
	}
}

func TestSudoPrefixesEveryCommand(t *testing.T) {
	r := nodeRunner()
	firstDeployHealth(r, "mymo-api-r1")
	opts := goodOpts()
	opts.Sudo = true
	Deploy(context.Background(), r, imageApp(), opts)
	for i, argv := range r.calls {
		if argv[0] != "sudo" || argv[1] != "-n" {
			t.Errorf("call %d not privileged: %v", i, argv)
		}
	}
}

func TestArchiveSkipsGitAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM scratch\n")
	write(".git/config", "[core]\n")
	write("app/main.go", "package main\n")

	tarball, err := archiveBuildContext(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	gz, err := gzip.NewReader(tarball)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, "Dockerfile") || !strings.Contains(joined, "app/main.go") {
		t.Errorf("the archive lost the project:\n%s", joined)
	}
	if strings.Contains(joined, ".git") {
		t.Errorf(".git traveled to the node:\n%s", joined)
	}
}

// An interrupted deploy leaves an orphaned staging container: mymo
// died after docker run but before the record was written, so the
// next attempt computes the same release id and the name collides.
// The engine detects its own leftover at the door — the container
// name is by construction this release's — removes it, says so, and
// the release starts fresh.
func TestInterruptedDeployIsRecoveredAtTheNextAttempt(t *testing.T) {
	app := imageApp()
	r := nodeRunner()
	// the interrupted attempt left r1's container on the node; the
	// record knows nothing about it
	r.responses["docker inspect mymo-api-r1"] = `{"Name":"mymo-api-r1","State":{"Status":"exited"}}`
	firstDeployHealth(r, "mymo-api-r1")

	var narrated []string
	o := goodOpts()
	o.Progress = func(line string) { narrated = append(narrated, line) }

	res := Deploy(context.Background(), r, app, o)
	if !res.OK {
		t.Fatalf("the interrupted deploy was not recovered (failed at %s):\n%s", res.FailedAt, r.callList())
	}
	if !r.called("docker rm -f mymo-api-r1") {
		t.Fatal("the orphaned staging container was left in the way")
	}
	said := false
	for _, line := range narrated {
		if strings.Contains(line, "interrupted r1 staging container") {
			said = true
		}
	}
	if !said {
		t.Errorf("the recovery was silent:\n%s", strings.Join(narrated, "\n"))
	}
	if len(res.App.Releases) != 1 || res.App.Releases[0].ID != 1 ||
		res.App.Releases[0].State != domain.ReleaseActive {
		t.Errorf("releases = %+v", res.App.Releases)
	}
}

// When the leftover cannot be removed — a stuck container, a
// permissions drift — the deploy refuses before starting anything,
// and says exactly what stands in the way and whose it is.
func TestUnremovableLeftoverRefusesBeforeStarting(t *testing.T) {
	app := imageApp()
	r := nodeRunner()
	r.responses["docker inspect mymo-api-r1"] = `{"Name":"mymo-api-r1"}`
	r.codes["docker rm -f mymo-api-r1"] = 1
	firstDeployHealth(r, "mymo-api-r1")

	res := Deploy(context.Background(), r, app, goodOpts())
	if res.OK || res.FailedAt != "stage" {
		t.Fatalf("a stuck leftover let the deploy through: ok=%v failedAt=%s", res.OK, res.FailedAt)
	}
	if len(res.Steps) == 0 || !strings.Contains(res.Steps[len(res.Steps)-1].Note, "interrupted mid-staging") {
		t.Errorf("the refusal does not say what stands in the way:\n%+v", res.Steps)
	}
	if r.called("docker run -d") {
		t.Fatal("a second container was started on top of a stuck leftover")
	}
}
