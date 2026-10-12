package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/deploy"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/manifest"
	"github.com/elvonpiko/mymo/internal/state"
)

// cannedAppRunner answers the application verbs' commands: the
// privilege probe, the container status, the log tail, restart.
type cannedAppRunner struct {
	calls   []string
	running bool
	logs    string
	failCmd string
}

func (r *cannedAppRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	joined := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, joined)
	switch {
	case joined == "id -u":
		return "0\n", 0, nil // root: no sudo prefix anywhere
	case name == r.failCmd:
		return "boom\n", 1, fmt.Errorf("exit 1")
	case strings.HasPrefix(joined, "docker ps"):
		if r.running {
			return "mymo-api-r2\n", 0, nil
		}
		return "\n", 0, nil
	case strings.HasPrefix(joined, "docker logs"):
		return r.logs, 0, nil
	case strings.HasPrefix(joined, "docker restart"):
		return "mymo-api-r2\n", 0, nil
	}
	return "", 0, nil
}

// The verbs on the detail page run the CLI's exact commands: the
// status probe asks docker for the active container, and the
// page's live row states the answer.
func TestAppLiveStatusLandsOnThePage(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	runner := &cannedAppRunner{running: true}
	m := New(s)
	m.appRunner = runner
	m = press(t, m, "a")
	m = press(t, m, "enter") // the api app is the only row

	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // the first verb: Live status
	m = drain(t, m, msgs)

	out := ansiStrip(view(m))
	if !strings.Contains(out, "running right now") {
		t.Errorf("the live row should state the container is running:\n%s", out)
	}
	// the probe is read-only and exactly the CLI's question
	saw := false
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "docker ps --filter name=mymo-api-r2 --filter status=running") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("the probe never asked docker for the container: %v", runner.calls)
	}
}

// A container that is not running says so in the error color —
// status is a question about reality, and reality gets to say no.
func TestAppLiveStatusSaysNotRunning(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	m := New(s)
	m.appRunner = &cannedAppRunner{running: false}
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)

	out := ansiStrip(view(m))
	if !strings.Contains(out, "NOT running") {
		t.Errorf("a stopped container should say so:\n%s", out)
	}
}

// Logs land in their own page — the title says whose tail it is,
// the way out is pinned, and j walks the lines.
func TestAppLogsOpenInTheirOwnPage(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "2026-07-%02d line %d of the tail\n", i+1, i)
	}
	m := New(s)
	m.appRunner = &cannedAppRunner{logs: b.String()}
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j") // walk to Logs
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)

	if m.cur().kind != scAppLogs {
		t.Fatalf("the logs did not open their page: %d", m.cur().kind)
	}
	out := ansiStrip(view(m))
	for _, want := range []string{"Logs", "api", "line 39 of the tail", "esc back", "j/k scroll"} {
		if !strings.Contains(out, want) {
			t.Errorf("the logs page is missing %q:\n%s", want, out)
		}
	}
	// the window opens at the tail — the newest lines are the ones
	// on screen, the oldest are one j away
	if strings.Contains(out, "line 0 of the tail") {
		t.Errorf("the window opened at the head, not the tail:\n%s", out)
	}
	if !strings.Contains(out, "line 26 of the tail") {
		t.Errorf("the tail window should hold the newest lines:\n%s", out)
	}
	m = press(t, m, "k")
	out = ansiStrip(view(m))
	if strings.Contains(out, "line 39 of the tail") && strings.Contains(out, "line 20 of the tail") {
		t.Errorf("k should walk the window up:\n%s", out)
	}

	// esc returns to the app's page
	m = press(t, m, "esc")
	if m.cur().kind != scAppDetail {
		t.Errorf("esc did not return to the app's page: %d", m.cur().kind)
	}
}

// Restart runs docker restart on the active release and toasts the
// outcome; a refusal toasts the failure with its reason.
func TestAppRestartTellsTheOutcome(t *testing.T) {
	s := readyStore(t)
	n := seedNode(t, s, "web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: baseline.Version, Verdict: "ready"}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	seedApp(t, s, sampleApp("api", "web-1"))
	m := New(s)
	m.appRunner = &cannedAppRunner{}
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j", "j") // walk to Restart
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)

	if !strings.Contains(m.toast.text, "restarted") {
		t.Errorf("a clean restart should toast its outcome: %q", m.toast.text)
	}
	saw := false
	for _, c := range m.appRunner.(*cannedAppRunner).calls {
		if strings.HasPrefix(c, "docker restart mymo-api-r2") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("restart never ran docker restart: %v", m.appRunner.(*cannedAppRunner).calls)
	}
}

// mymo only operates on a node it has verified: the mutating verbs
// refuse on a node that is not READY, before any command runs.
func TestAppVerbsRefuseOnUnverifiedNode(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	runner := &cannedAppRunner{}
	m := New(s)
	m.appRunner = runner
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j", "j") // Restart
	m = press(t, m, "enter")

	if strings.Contains(m.toast.text, "restarted") {
		t.Errorf("restart ran on an unverified node: %q", m.toast.text)
	}
	if !strings.Contains(m.toast.text, "mymo only operates on a node it has verified") {
		t.Errorf("the refusal should name the rule: %q", m.toast.text)
	}
	if len(runner.calls) != 0 {
		t.Errorf("the refusal should run nothing: %v", runner.calls)
	}
}

// An app with nothing live has nothing to operate: the card says so
// instead of offering verbs that cannot work.
func TestAppWithoutActiveReleaseIsHonest(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	app := sampleApp("api", "web-1")
	app.Releases = nil
	seedApp(t, s, app)

	m := press(t, New(s), "a")
	m = press(t, m, "enter")
	out := ansiStrip(view(m))
	if !strings.Contains(out, "no releases yet") {
		t.Errorf("the record should say nothing was deployed:\n%s", out)
	}
	if !strings.Contains(out, "nothing to operate") {
		t.Errorf("the verbs card should say why it is empty:\n%s", out)
	}
}

// A long release history folds behind a line that says how many
// folded and where the whole truth lives — never a silent clip.
func TestAppRecordFoldsLongHistoriesHonestly(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	app := sampleApp("api", "web-1")
	app.Releases = app.Releases[:1]
	for i := 2; i <= 9; i++ {
		st := domain.ReleaseRetired
		if i == 9 {
			st = domain.ReleaseActive
		}
		app.Releases = append(app.Releases, domain.Release{
			ID: i, State: st, Container: fmt.Sprintf("mymo-api-r%d", i),
			Digest: fmt.Sprintf("sha256:%012d", i), Health: "ok",
		})
	}
	seedApp(t, s, app)

	m := New(s)
	m = press(t, m, "a")
	m = press(t, m, "enter")
	out := ansiStrip(view(m))
	if !strings.Contains(out, "folded — mymo app status api lists every one") {
		t.Errorf("the fold should state itself and where the whole truth lives:\n%s", out)
	}
	if !strings.Contains(out, "r9") || !strings.Contains(out, "r8") {
		t.Errorf("the active and recent releases should stay:\n%s", out)
	}
	if strings.Contains(out, "r2") {
		t.Errorf("a folded release should not render:\n%s", out)
	}
}

// The typed name is the gate: a wrong name runs nothing and clears
// the buffer; the exact name runs the engine, writes the record,
// and states which release is live again.
func TestAppRollbackWantsTheExactName(t *testing.T) {
	s := readyStore(t)
	n := seedNode(t, s, "web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: baseline.Version, Verdict: "ready"}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	seedApp(t, s, sampleApp("api", "web-1"))
	m := New(s)
	m.appRunner = &cannedAppRunner{}
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j", "j", "j") // walk to Rollback
	m = press(t, m, "enter")
	if m.cur().kind != scAppRollback {
		t.Fatalf("the rollback gate did not open: %d", m.cur().kind)
	}
	out := ansiStrip(view(m))
	for _, want := range []string{"Rollback", "release 2", "release 1", "type api in full"} {
		if !strings.Contains(out, want) {
			t.Errorf("the gate is missing %q:\n%s", want, out)
		}
	}

	// a wrong name refuses and clears the buffer
	m = press(t, m, "a", "x")
	m = press(t, m, "enter")
	if !strings.Contains(m.toast.text, "does not match api") {
		t.Errorf("a wrong name should refuse: %q", m.toast.text)
	}
	if m.rbTyped != "" {
		t.Errorf("a refused name should clear the buffer: %q", m.rbTyped)
	}
	app, err := s.GetApp("api")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := app.ActiveRelease(); got.ID != 2 {
		t.Errorf("a refused name changed the record: r%d is active", got.ID)
	}

	// the exact name runs the engine
	m = press(t, m, "a", "p", "i")
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)
	if !strings.Contains(m.toast.text, "release 1 is live again") {
		t.Errorf("a clean rollback should state its outcome: %q", m.toast.text)
	}
	if m.cur().kind != scAppDetail {
		t.Errorf("the gate should exit back to the app page: %d", m.cur().kind)
	}
	app, err = s.GetApp("api")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := app.ActiveRelease(); got.ID != 1 {
		t.Errorf("the record should say r1 is live again: r%d", got.ID)
	}
	retired := false
	for _, r := range app.Releases {
		if r.ID == 2 && r.State == domain.ReleaseRetired {
			retired = true
		}
	}
	if !retired {
		t.Errorf("the rolled-back release should be kept for rollback, not discarded")
	}
}

// Rollback refuses on a node mymo has not verified, before the gate
// or any command.
func TestAppRollbackRefusesUnverifiedNodes(t *testing.T) {
	s := readyStore(t)
	seedNode(t, s, "web-1")
	seedApp(t, s, sampleApp("api", "web-1"))
	runner := &cannedAppRunner{}
	m := New(s)
	m.appRunner = runner
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j", "j", "j")
	m = press(t, m, "enter")
	if m.cur().kind == scAppRollback {
		t.Errorf("the gate opened on an unverified node")
	}
	if !strings.Contains(m.toast.text, "mymo only operates on a node it has verified") {
		t.Errorf("the refusal should name the rule: %q", m.toast.text)
	}
	if len(runner.calls) != 0 {
		t.Errorf("the refusal should run nothing: %v", runner.calls)
	}
}

// esc aborts the gate without a trace — no command, no record.
func TestAppRollbackEscAborts(t *testing.T) {
	s := readyStore(t)
	n := seedNode(t, s, "web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: baseline.Version, Verdict: "ready"}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	seedApp(t, s, sampleApp("api", "web-1"))
	m := New(s)
	m.appRunner = &cannedAppRunner{}
	m = press(t, m, "a")
	m = press(t, m, "enter")
	m = press(t, m, "j", "j", "j")
	m = press(t, m, "enter")
	m = press(t, m, "a", "p") // half a name, then out
	m = press(t, m, "esc")
	if m.cur().kind != scAppDetail {
		t.Errorf("esc did not leave the gate: %d", m.cur().kind)
	}
	app, err := s.GetApp("api")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := app.ActiveRelease(); got.ID != 2 {
		t.Errorf("an aborted rollback changed the record: r%d", got.ID)
	}
}

// deployCannedRunner answers the engine's questions: the digest of
// a pulled image, and success for everything else.
type deployCannedRunner struct{ calls []string }

func (r *deployCannedRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	joined := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, joined)
	if joined == "id -u" {
		return "0" + "\n", 0, nil // root: no sudo prefix anywhere
	}
	if strings.HasPrefix(joined, "docker image inspect") {
		return "sha256:c0ffee0000000000000000000000000000000000000000000000000000000000\n", 0, nil
	}
	if strings.HasPrefix(joined, "docker network inspect") {
		return "\n", 1, nil // absent: the engine creates it
	}
	if strings.HasPrefix(joined, "docker ps") {
		return "mymo-web-r1\n", 0, nil // the container answers its own name
	}
	if strings.HasPrefix(joined, "systemctl is-active caddy") {
		return "active\n", 0, nil
	}
	return "", 0, nil
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := dir + "/" + name
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readyWebNode(t *testing.T, s *state.Store) {
	t.Helper()
	n := seedNode(t, s, "web-1")
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: baseline.Version, Verdict: "ready"}
	if err := s.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
}

const deployManifest = `# mymo: this app's deployable truth
name: web
type: image
image: nginx:1.27
port: 80
domain: web.example.com
health: /
`

// The whole flow, walked: the typed directory discovers the
// manifest, the verified host is picked, the card states the
// domain promise, the exact name runs the engine, the report states
// the outcome — and enter opens the app the deploy made.
func TestDeployFlowWalksTheWholePath(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	dir := writeProject(t, map[string]string{"mymo.yaml": deployManifest})

	m := New(s)
	m.appRunner = &deployCannedRunner{}
	m = press(t, m, "d")
	if m.cur().kind != scDeploy || m.dpStage != dpPath {
		t.Fatalf("the flow did not open at the path stage: %d/%d", m.cur().kind, m.dpStage)
	}
	m.dpPathText = dir
	m.dpLoadWalk()
	m = press(t, m, "enter")

	// a new app picks its node from the verified hosts
	if m.dpStage != dpPick {
		t.Fatalf("a new app should pick its node: %d (%s)", m.dpStage, m.dpErr)
	}
	out := ansiStrip(view(m))
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "web is new") {
		t.Errorf("the pick stage should list the verified host:\n%s", out)
	}
	m = press(t, m, "enter")

	// the confirm card states the domain promise — it is what gets
	// confirmed — and the one manual step: DNS points at the box
	if m.dpStage != dpConfirm {
		t.Fatalf("the gate did not arm: %d (%s)", m.dpStage, m.dpErr)
	}
	out = ansiStrip(view(m))
	for _, want := range []string{"web → web-1", "https://web.example.com through caddy",
		"point web.example.com at this box first", "type web in full"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirm card is missing %q:\n%s", want, out)
		}
	}

	// the exact name runs the engine
	m = press(t, m, "w", "e", "b")
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)

	if m.dpStage != dpReport {
		t.Fatalf("the engine did not land its report: %d (%s)", m.dpStage, m.dpDoneErr)
	}
	out = ansiStrip(view(m))
	for _, want := range []string{"DONE", "KEPT", "is live on web-1", "https://web.example.com",
		"tls is issued once the name resolves to this box", "enter opens the app"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report is missing %q:\n%s", want, out)
		}
	}
	if m.dpResult.OK != true {
		t.Errorf("the engine should have succeeded: %+v", m.dpResult.Steps)
	}

	// the finished report exits forward
	m = press(t, m, "enter")
	if m.cur().kind != scAppDetail || m.cur().app != "web" {
		t.Errorf("enter should open the deployed app: %d/%s", m.cur().kind, m.cur().app)
	}
	app, err := s.GetApp("web")
	if err != nil {
		t.Fatalf("the deploy did not record the app: %v", err)
	}
	if rel, _ := app.ActiveRelease(); rel.ID != 1 || rel.State != domain.ReleaseActive {
		t.Errorf("release 1 should be active: %+v", rel)
	}
}

// A public hostname belongs to one app: the conflict is refused
// before the gate, with the owner named.
func TestDeployFlowRefusesATakenDomain(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	taken := sampleApp("api", "web-1")
	seedApp(t, s, taken)
	dir := writeProject(t, map[string]string{"mymo.yaml": strings.Replace(
		deployManifest, "web.example.com", "api.example.com", 1)})

	m := New(s)
	m.appRunner = &deployCannedRunner{}
	m = press(t, m, "d")
	m.dpPathText = dir
	m.dpLoadWalk()
	m = press(t, m, "enter")
	m = press(t, m, "enter") // pick web-1

	if m.dpStage == dpConfirm {
		t.Errorf("the gate armed on a taken domain")
	}
	if !strings.Contains(m.dpErr, "api.example.com") || !strings.Contains(m.dpErr, "belongs to api") {
		t.Errorf("the refusal should name the owner: %q", m.dpErr)
	}
}

// A bare Dockerfile earns a draft to finish — mymo does not guess.
func TestDeployFlowShowsTheDraftHonestly(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	dir := writeProject(t, map[string]string{"Dockerfile": "FROM alpine\n"})

	m := press(t, New(s), "d")
	m.dpPathText = dir
	m.dpLoadWalk()
	m.dpLoadWalk()
	m = press(t, m, "enter")

	out := ansiStrip(view(m))
	if !strings.Contains(out, "has a Dockerfile but no mymo.yaml") {
		t.Errorf("the draft card should state the situation:\n%s", out)
	}
	if !strings.Contains(out, "write this draft to mymo.yaml") {
		t.Errorf("the draft should be shown to finish:\n%s", out)
	}
	if m.dpStage != dpPath {
		t.Errorf("the flow should stay at the path stage: %d", m.dpStage)
	}
}

// No verified hosts means no deploy — the refusal says what to do.
func TestDeployFlowNeedsAVerifiedHost(t *testing.T) {
	s := readyStore(t)
	dir := writeProject(t, map[string]string{"mymo.yaml": deployManifest})

	m := press(t, New(s), "d")
	m.dpPathText = dir
	m.dpLoadWalk()
	m.dpLoadWalk()
	m = press(t, m, "enter")

	if !strings.Contains(m.dpErr, "no verified app host") {
		t.Errorf("the refusal should name what is missing: %q", m.dpErr)
	}
	if m.dpStage != dpPath {
		t.Errorf("the flow should stay at the path stage: %d", m.dpStage)
	}
}

// An existing app's node is fixed — discovery goes straight to the
// gate, no pick stage.
func TestDeployFlowExistingAppSkipsThePick(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	seedApp(t, s, sampleApp("web", "web-1"))
	dir := writeProject(t, map[string]string{"mymo.yaml": deployManifest})

	m := New(s)
	m.appRunner = &deployCannedRunner{}
	m = press(t, m, "d")
	m.dpPathText = dir
	m.dpLoadWalk()
	m = press(t, m, "enter")

	if m.dpStage != dpConfirm {
		t.Fatalf("an existing app should go straight to the gate: %d (%s)", m.dpStage, m.dpErr)
	}
	out := ansiStrip(view(m))
	if !strings.Contains(out, "release 2 · kept") {
		t.Errorf("the card should say what is current:\n%s", out)
	}
}

// The walker finds the project instead of asking for it to be
// typed: the markers say what lives where, enter on a marked
// directory selects it, and a plain one descends.
func TestDeployWalkerFindsTheProject(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	tmp := writeProject(t, map[string]string{
		"root/manifested/mymo.yaml": deployManifest,
		"root/dockered/Dockerfile":  "FROM alpine\n",
		"root/emptydir/keep":        "",
	})
	root := filepath.Join(tmp, "root")
	m := press(t, New(s), "d")
	m.dpPathText = root
	m.dpLoadWalk()

	// every directory is marked with what a deploy would find
	out := ansiStrip(view(m))
	for _, want := range []string{
		"this directory", "mymo.yaml",
		"manifested", "dockered", "emptydir",
		"Dockerfile — draft",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the walker is missing %q:\n%s", want, out)
		}
	}

	// enter on a plain directory descends; u climbs back
	m = press(t, m, "j", "j", "j") // emptydir: after this-directory, .., dockered
	m = press(t, m, "enter")
	if m.dpPathText != filepath.Join(root, "emptydir") {
		t.Fatalf("enter did not descend: %s", m.dpPathText)
	}
	m = press(t, m, "u")
	if m.dpPathText != root {
		t.Fatalf("u did not climb: %s", m.dpPathText)
	}

	// enter on a manifest-marked directory selects it as the project
	m = press(t, m, "j") // .. after this-directory
	// walk to "manifested": entries are this-directory, .., dockered,
	// emptydir, manifested (sorted)
	m = press(t, m, "j", "j", "j", "j")
	if e := m.dpWalkEntries[m.dpWalkCursor].name; e != "manifested" {
		t.Fatalf("the cursor is not on manifested: %q", e)
	}
	m, msgs := drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, msgs)
	if m.dpStage != dpPick {
		t.Fatalf("a marked directory should become the project: %d (%s)", m.dpStage, m.dpErr)
	}
}

// Both engine ceremonies arm the spinner before they run: without
// the tick the loading page renders once and freezes — the clock
// still, the spinner still — while the engine works, and a working
// deploy reads as a hang.
func TestEngineCeremoniesArmTheSpinner(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	app := sampleApp("api", "web-1")
	seedApp(t, s, app)
	node, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(s)
	m.appRunner = &cannedAppRunner{}

	// the rollback engine
	_, cmd := m.runAppRollback(app, node, app.Releases[0])
	assertArmedTick(t, cmd, "rollback")

	// the deploy engine
	m.dpMan = manifest.Manifest{Name: "api", Type: "image", Image: "ghcr.io/x/api:1.2.3", Port: 8080}
	m.dpExisting = true
	m.dpTargetApp = app
	_, cmd = m.runDeployEngine()
	assertArmedTick(t, cmd, "deploy")
}

// assertArmedTick runs one engine batch and demands the spinner's
// tick among its commands — the tick animates the clock and the
// spinner; without it the ceremony renders once and goes still.
func assertArmedTick(t *testing.T, cmd tea.Cmd, what string) {
	t.Helper()
	res, ok := runCmdTimed(cmd, 400*time.Millisecond)
	if !ok {
		t.Fatalf("%s: the batch never resolved", what)
	}
	batch, is := res.(tea.BatchMsg)
	if !is {
		t.Fatalf("%s: the batch yielded %T, not commands", what, res)
	}
	for _, c := range batch {
		if msg, ok := runCmdTimed(c, 300*time.Millisecond); ok {
			if _, is := msg.(spinner.TickMsg); is {
				return
			}
		}
	}
	t.Errorf("%s: the ceremony never armed the spinner — the page would freeze", what)
}

// The report's reason never dies mid-word: a note that does not fit
// its row wraps underneath, whole, with the rest of its lines
// folded and counted — "c..." hiding "context deadline exceeded"
// sent a real deploy's diagnosis down the drain.
func TestDeployReportKeepsTheWholeReason(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	app := sampleApp("web", "web-1")
	app.Releases = nil
	node, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(s)
	m.push(screen{kind: scDeploy})
	m.dpStage = dpReport
	m.dpResult = deploy.Result{
		App: app,
		Steps: []deploy.StepResult{
			{Title: "docker network mymo-net", State: deploy.StepKept, Note: "already exists"},
			{
				Title: "dockerfile .",
				State: deploy.StepFailed,
				Note:  "transferring the build context failed: ssh: copy to /tmp/mymo-build/web-r1: context deadline exceeded",
			},
		},
	}
	_ = node
	out := ansiStrip(view(m))
	if !strings.Contains(out, "context deadline exceeded") {
		t.Errorf("the reason was cut:\n%s", out)
	}
	if strings.Contains(out, "c...") || strings.Contains(out, "c…") {
		t.Errorf("the reason still dies mid-word:\n%s", out)
	}
	// a short note stays on its row: the happy report keeps its height
	if !strings.Contains(out, "already exists") {
		t.Errorf("a fitting note left its row:\n%s", out)
	}
}

// A failed build gets its layer named on the report: whose fix it
// is — the project's or the box's — stated in plain words under
// the failure, so the operator knows where to work before they
// reread the note.
func TestDeployReportNamesWhoseFixItIs(t *testing.T) {
	s := readyStore(t)
	readyWebNode(t, s)
	m := New(s)
	m.push(screen{kind: scDeploy})
	m.dpStage = dpReport
	m.dpResult = deploy.Result{
		App:      sampleApp("web", "web-1"),
		FailedAt: "build",
		Steps: []deploy.StepResult{
			{
				Title: "dockerfile .",
				State: deploy.StepFailed,
				Note:  "main.go:18:2: missing go.sum entry for module providing package github.com/go-chi/chi/v5; to add: go get example.com/simple-go-web",
			},
		},
	}
	out := ansiStrip(view(m))
	if !strings.Contains(out, "the failure is inside the project's build") {
		t.Errorf("the report did not name whose fix it is:\n%s", out)
	}

	// a network failure inside the build is the box's, not the
	// project's — the sentence follows the note
	m.dpResult.Steps[0].Note = "go: dial tcp: lookup proxy.golang.org: i/o timeout — go mod download failed"
	out = ansiStrip(view(m))
	if !strings.Contains(out, "the box's docker networking, not the app's code") {
		t.Errorf("a network failure should be named the box's:\n%s", out)
	}

	// a registry's refusal names the FROM reference — a third
	// layer, stated as plainly as the other two
	m.dpResult.Steps[0].Note = "ERROR: unexpected status from HEAD request to https://cgr.dev/chainguard/static/manifests/latest: 403 Forbidden"
	out = ansiStrip(view(m))
	if !strings.Contains(out, "the registry refused the image the Dockerfile names") {
		t.Errorf("a registry refusal should name the FROM reference:\n%s", out)
	}
}
