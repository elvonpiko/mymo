package plan

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/preflight"
)

type fakeRunner struct {
	responses map[string]string
	asked     []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	key := name + " " + strings.Join(args, " ")
	r.asked = append(r.asked, key)
	if out, ok := r.responses[key]; ok {
		return out, 0, nil
	}
	return "", 1, nil
}

// ubuntuRunner answers the one command generation runs: the
// distribution capture.
func ubuntuRunner() *fakeRunner {
	return &fakeRunner{responses: map[string]string{
		`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`: "ubuntu noble",
	}}
}

func cleanNode() domain.Node {
	return domain.Node{Name: "web-1", Host: "203.0.113.10", User: "amir", Mode: domain.ModeObserve}
}

func cleanAudit() preflight.Audit {
	return preflight.Audit{
		UID: 1000, Sudo: true, Firewall: "absent",
		SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}},
		Listeners:     map[int]string{},
	}
}

func cleanSnapshot() facts.Node {
	return facts.Node{
		Hostname: "web-1", OS: "Ubuntu 24.04.2 LTS", Arch: "x86_64",
		Systemd: true, User: "amir",
		MemTotal: 4 << 30, DiskTotal: 40 << 30, DiskFree: 32 << 30,
		CollectedAt: time.Now(),
	}
}

func TestGenerateCleanUbuntuPlan(t *testing.T) {
	r := ubuntuRunner()
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	want := []string{
		"mymo-user", "ssh-hardening", "sysctl", "unattended-upgrades",
		"firewall", "docker", "docker-daemon", "caddy", "state-dir",
	}
	if len(steps) != len(want) {
		t.Fatalf("plan has %d steps, want %d", len(steps), len(want))
	}
	for i, id := range want {
		if steps[i].Control != id {
			t.Errorf("step %d = %q, want %q", i, steps[i].Control, id)
		}
	}

	// every step says what it does, and every detail stays a single
	// review line
	for _, s := range steps {
		if s.Title == "" || s.Detail == "" {
			t.Errorf("step %q is incomplete: %+v", s.Control, s)
		}
		if lipglossWidth(s.Detail) > 53 {
			t.Errorf("step %q detail too long for one review line: %q", s.Control, s.Detail)
		}
	}

	// the sshd drop-in is the exact file sshd will load
	var ssh Step
	for _, s := range steps {
		if s.Control == "ssh-hardening" {
			ssh = s
		}
	}
	dropin := ssh.Files[0].Content
	for _, want := range []string{
		"PermitRootLogin no\n",
		"PasswordAuthentication no\n",
		"AllowTcpForwarding no\n",
		"Match User amir,mymo\n",
		"    AllowTcpForwarding yes\n",
		"Match all\n",
	} {
		if !strings.Contains(dropin, want) {
			t.Errorf("drop-in missing %q:\n%s", want, dropin)
		}
	}
	if ssh.Gate == "" {
		t.Error("the sshd step must carry the second-connection gate")
	}
	// the operator's way in stays open until the gate passes: the
	// reload is the last command
	if got := ssh.Exec[len(ssh.Exec)-1]; strings.Join(got, " ") != "systemctl reload ssh" {
		t.Errorf("sshd step ends with %v, want the reload", got)
	}

	// the sysctl floor includes the pinned rp_filter
	var sys Step
	for _, s := range steps {
		if s.Control == "sysctl" {
			sys = s
		}
	}
	if !strings.Contains(sys.Files[0].Content, "net.ipv4.conf.all.rp_filter = 2\n") {
		t.Errorf("sysctl file missing loose rp_filter:\n%s", sys.Files[0].Content)
	}

	// docker from the official repository, never docker.io, never a
	// piped script — and the install carries the packages as separate
	// arguments
	var docker Step
	for _, s := range steps {
		if s.Control == "docker" {
			docker = s
		}
	}
	// the repo line is pinned in the command that writes it —
	// docker's own repo, this architecture, this codename
	var repoLine string
	for _, argv := range docker.Exec {
		if j := strings.Join(argv, " "); strings.Contains(j, "sources.list.d/docker.list") && strings.Contains(j, "printf") {
			repoLine = j
		}
	}
	if !strings.Contains(repoLine, "https://download.docker.com/linux/ubuntu noble stable") ||
		!strings.Contains(repoLine, "arch=amd64") {
		t.Errorf("docker source list wrong: %q", repoLine)
	}
	var install []string
	for _, argv := range docker.Exec {
		if len(argv) > 3 && argv[0] == "apt-get" && argv[1] == "install" {
			install = argv
		}
	}
	if len(install) != 3+len(baseline.DockerPackages) {
		t.Errorf("docker install argv = %v (packages must be separate arguments)", install)
	}
	for _, banned := range []string{"docker.io", "get.docker.com"} {
		var all strings.Builder
		for _, f := range docker.Files {
			all.WriteString(f.Content)
		}
		for _, argv := range docker.Exec {
			all.WriteString(strings.Join(argv, " "))
		}
		if strings.Contains(all.String(), banned) {
			t.Errorf("docker step references banned %q", banned)
		}
	}

	// the daemon policy lands as its own step
	var daemon Step
	for _, s := range steps {
		if s.Control == "docker-daemon" {
			daemon = s
		}
	}
	if !strings.Contains(daemon.Files[0].Content, `"live-restore": true`) {
		t.Errorf("daemon.json missing live-restore: %q", daemon.Files[0].Content)
	}

	// caddy from cloudsmith, systemd-managed
	var caddy Step
	for _, s := range steps {
		if s.Control == "caddy" {
			caddy = s
		}
	}
	// caddy is the official static binary: no third-party apt
	// repository at all, the release archive verified by its own
	// digest, run under the official systemd unit
	var all strings.Builder
	for _, argv := range caddy.Exec {
		all.WriteString(strings.Join(argv, " ") + "\n")
	}
	caddyExec := all.String()
	for _, want := range []string{
		baseline.CaddyReleaseBase,
		"sha512sum -c",
		"install -m 0755 /tmp/caddy /usr/bin/caddy",
		"useradd --system --gid caddy",
		"systemctl daemon-reload",
		"install -d /etc/caddy/mymo",
		"/etc/caddy/mymo/Placeholder.caddy",
		"printf 'import mymo/*.caddy\n' >> /etc/caddy/Caddyfile",
		// the cloudsmith repo is moved aside wherever it came from:
		// while it stands it 402s every apt on the box
		"mv /etc/apt/sources.list.d/caddy-stable.list /etc/apt/sources.list.d/caddy-stable.list.pre-mymo",
	} {
		if !strings.Contains(caddyExec, want) {
			t.Errorf("the caddy setup misses its official recipe (%s):\n%s", want, caddyExec)
		}
	}
	if strings.Contains(caddyExec, "dl.cloudsmith.io/public") {
		t.Errorf("the caddy step still talks to the cloudsmith repository:\n%s", caddyExec)
	}
	// the config exists before the service starts: a caddy pointed
	// at a Caddyfile that is not there dies on arrival
	importAt, startAt := -1, -1
	for i, argv := range caddy.Exec {
		j := strings.Join(argv, " ")
		if strings.Contains(j, ">> /etc/caddy/Caddyfile") {
			importAt = i
		}
		if strings.Contains(j, "enable --now caddy") {
			startAt = i
		}
	}
	if importAt < 0 || startAt < 0 || importAt > startAt {
		t.Errorf("the Caddyfile (cmd %d) is not in place before caddy starts (cmd %d)", importAt, startAt)
	}
	// the official systemd unit, byte for byte
	if len(caddy.Files) != 1 || caddy.Files[0].Path != "/etc/systemd/system/caddy.service" ||
		!strings.Contains(caddy.Files[0].Content, "ExecStart=/usr/bin/caddy run --environ --config /etc/caddy/Caddyfile") {
		t.Errorf("the official systemd unit is not the caddy step's file: %+v", caddy.Files)
	}

	// the marker names every control
	var state Step
	for _, s := range steps {
		if s.Control == "state-dir" {
			state = s
		}
	}
	for _, c := range baseline.Controls {
		if !strings.Contains(state.Files[0].Content, c.ID) {
			t.Errorf("baseline marker missing control %q", c.ID)
		}
	}
}

func TestGenerateIsReadOnly(t *testing.T) {
	r := ubuntuRunner()
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(r.asked) != 1 {
		t.Fatalf("generation ran %d commands, want 1: %q", len(r.asked), r.asked)
	}
	for _, cmd := range r.asked {
		lower := strings.ToLower(cmd)
		for _, banned := range []string{"rm ", "apt-get", "systemctl", "tee ", "ufw ", "useradd", "curl", "install "} {
			if strings.Contains(lower, banned) {
				t.Errorf("generation ran a mutating command: %q (matched %q)", cmd, banned)
			}
		}
	}
}

// A decided audit plans: the operator took the call, apply
// re-verifies what was accepted. The generation itself stays
// read-only either way — deciding changes the draft, not the box.
func TestGenerateDecidedDecisionsPlan(t *testing.T) {
	audit := cleanAudit()
	audit.Listeners = map[int]string{80: "nginx"}
	audit.DockerPkg = "docker.io"
	r := ubuntuRunner()
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), audit, true)
	if err != nil {
		t.Fatalf("a decided audit should plan: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("the plan came back empty")
	}
	if len(r.asked) != 1 {
		t.Fatalf("generation ran %d commands, want 1: %q", len(r.asked), r.asked)
	}
	for _, cmd := range r.asked {
		lower := strings.ToLower(cmd)
		for _, banned := range []string{"rm ", "apt-get", "systemctl", "tee ", "ufw ", "useradd", "curl", "install "} {
			if strings.Contains(lower, banned) {
				t.Errorf("generation ran a mutating command: %q (matched %q)", cmd, banned)
			}
		}
	}
}

// An abort is never decidable: the box cannot carry the baseline,
// and no operator answer changes that.
func TestGenerateAbortsRefuseEvenWhenDecided(t *testing.T) {
	snap := cleanSnapshot()
	snap.OS = ""
	snap.Arch = ""
	_, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), snap, cleanAudit(), true)
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("a decided abort still refused: %v", err)
	}
}

func TestGenerateBlockedByDecisions(t *testing.T) {
	audit := cleanAudit()
	audit.Listeners = map[int]string{80: "nginx"}
	audit.DockerPkg = "docker.io"
	_, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), cleanSnapshot(), audit, false)

	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("Generate returned %v, want BlockedError", err)
	}
	if len(blocked.Decisions) < 2 {
		t.Errorf("blocked error lists %d decisions, want the ports and docker ones", len(blocked.Decisions))
	}
	var sawPorts, sawDocker bool
	for _, c := range blocked.Decisions {
		if c.Title == "ports" {
			sawPorts = true
		}
		if c.Title == "docker" {
			sawDocker = true
		}
	}
	if !sawPorts || !sawDocker {
		t.Errorf("blocked error = %v", blocked)
	}
	if !strings.Contains(blocked.Error(), "decisions before planning") {
		t.Errorf("blocked message unclear: %q", blocked.Error())
	}
}

func TestGenerateRefusesHostileOperatorName(t *testing.T) {
	n := cleanNode()
	n.User = "amir; PermitRootLogin yes"
	_, err := Generate(context.Background(), ubuntuRunner(), n, cleanSnapshot(), cleanAudit(), false)
	if err == nil || !strings.Contains(err.Error(), "not a valid unix name") {
		t.Fatalf("hostile operator accepted: %v", err)
	}
}

func TestGenerateRootOperatorWarnsAndCarvesOnlyMymo(t *testing.T) {
	// the operator is the account on the node's record; the snapshot
	// confirms the same login was discovered on the box
	n := cleanNode()
	n.User = "root"
	snap := cleanSnapshot()
	snap.User = "root"
	steps, err := Generate(context.Background(), ubuntuRunner(), n, snap, cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var ssh Step
	for _, s := range steps {
		if s.Control == "ssh-hardening" {
			ssh = s
		}
	}
	if !strings.Contains(ssh.Detail, "no root ssh") {
		t.Errorf("root lockout not stated: %q", ssh.Detail)
	}
	if !strings.Contains(ssh.Files[0].Content, "Match User mymo\n") {
		t.Errorf("root must not be carved out:\n%s", ssh.Files[0].Content)
	}
}

func TestGenerateAdoptsInsteadOfReinstalling(t *testing.T) {
	// docker-ce present with no daemon config, caddy with no Caddyfile
	snap := cleanSnapshot()
	snap.Docker = "Docker version 27.3.1, build abc"
	snap.Caddy = "v2.8.4 h1:..."
	audit := cleanAudit()
	audit.DockerPkg = "docker-ce"

	steps, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), snap, audit, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var docker, daemon, caddy Step
	for _, s := range steps {
		switch s.Control {
		case "docker":
			docker = s
		case "docker-daemon":
			daemon = s
		case "caddy":
			caddy = s
		}
	}
	if len(docker.Files) != 0 || len(docker.Packages) != 0 {
		t.Errorf("docker adopt must not reinstall: %+v", docker)
	}
	if !strings.Contains(docker.Title, "adopt") {
		t.Errorf("docker step title = %q", docker.Title)
	}
	if strings.Join(daemon.Exec[0], " ") != "systemctl restart docker" {
		t.Errorf("daemon policy on an adopt must restart, got %v", daemon.Exec)
	}
	if !strings.Contains(daemon.Detail, "containers") {
		t.Errorf("the restart must say what it re-runs: %q", daemon.Detail)
	}
	if len(caddy.Files) != 0 || len(caddy.Packages) != 0 {
		t.Errorf("caddy adopt must not reinstall: %+v", caddy)
	}
	if !strings.Contains(caddy.Detail, "v2.8.4") {
		t.Errorf("caddy adopt detail = %q", caddy.Detail)
	}
}

func TestGenerateKeepsActiveFirewallDefaults(t *testing.T) {
	audit := cleanAudit()
	audit.Firewall = "active"
	steps, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), cleanSnapshot(), audit, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var fw Step
	for _, s := range steps {
		if s.Control == "firewall" {
			fw = s
		}
	}
	var all strings.Builder
	for _, argv := range fw.Exec {
		all.WriteString(strings.Join(argv, " ") + ";")
	}
	for _, banned := range []string{"default deny", "default allow", "--force enable"} {
		if strings.Contains(all.String(), banned) {
			t.Errorf("active firewall must keep its defaults: %q in %q", banned, all.String())
		}
	}
	if !strings.Contains(all.String(), "limit 22/tcp") {
		t.Errorf("ssh rate limit missing from active firewall: %q", all.String())
	}
}

func TestGenerateDebianPaths(t *testing.T) {
	r := ubuntuRunner()
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = "debian bookworm"
	snap := cleanSnapshot()
	snap.OS = "Debian GNU/Linux 12 (bookworm)"
	steps, err := Generate(context.Background(), r, cleanNode(), snap, cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, s := range steps {
		if s.Control == "docker" && len(s.Files) > 0 {
			if !strings.Contains(s.Files[0].Content, "https://download.docker.com/linux/debian bookworm stable") {
				t.Errorf("debian docker source wrong: %q", s.Files[0].Content)
			}
		}
	}
}

// Debian 13 rides the same live-codename path, its docker source
// pointing at the trixie repository the box itself names.
func TestGenerateDebian13TrixiePaths(t *testing.T) {
	r := ubuntuRunner()
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = "debian trixie"
	snap := cleanSnapshot()
	snap.OS = "Debian GNU/Linux 13 (trixie)"
	steps, err := Generate(context.Background(), r, cleanNode(), snap, cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, s := range steps {
		if s.Control == "docker" && len(s.Files) > 0 {
			if !strings.Contains(s.Files[0].Content, "https://download.docker.com/linux/debian trixie stable") {
				t.Errorf("trixie docker source wrong: %q", s.Files[0].Content)
			}
		}
	}
}

func TestGenerateRefusesUnknownCodename(t *testing.T) {
	r := ubuntuRunner()
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = ""
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false); err == nil {
		t.Fatal("missing codename accepted")
	}
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = "alpine edge"
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false); err == nil {
		t.Fatal("unsupported distribution id accepted")
	}
}

// lipglossWidth stands in for lipgloss.Width to keep the plan package
// free of UI dependencies; display width of plain ASCII copy is its
// rune count for our purposes.
func lipglossWidth(s string) int { return len([]rune(s)) }

func TestGenerateResumesAnInterruptedBootstrap(t *testing.T) {
	// the audit of a node whose bootstrap stopped mid-run: the mymo
	// user and state dir exist, the sudoers rule is mymo's, but the
	// marker was never reached
	r := ubuntuRunner()
	a := cleanAudit()
	a.MymoUserUsed = true
	a.MymoDirUsed = true
	a.MymoSudoers = baseline.MymoSudoersRule
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), a, false)
	if err != nil {
		t.Fatalf("Generate refused an interrupted bootstrap: %v", err)
	}
	// the plan regenerates in full — apply keeps whatever already
	// matches, so resume is the same honest path as the first run
	if len(steps) == 0 {
		t.Fatal("no steps generated")
	}
	found := false
	for _, s := range steps {
		if s.Control == "mymo-user" {
			found = true
		}
	}
	if !found {
		t.Error("the mymo-user step is missing from the resume plan")
	}
}

// TestMyMoUserStepSurvivesItsOwnResume proves the fix the second
// live test forced: a failed apply that had already created the mymo
// user stranded the box — a re-apply ran useradd again, sshd refused
// (exit 6, user exists), and mymo could not heal what it started.
// The step's first command must be a no-op when its work is done.
func TestMyMoUserStepSurvivesItsOwnResume(t *testing.T) {
	step := mymoUserStep()
	if len(step.Exec) == 0 {
		t.Fatal("the mymo user step has no commands")
	}
	first := strings.Join(step.Exec[0], " ")
	if !strings.Contains(first, "id mymo") || !strings.Contains(first, "useradd") {
		t.Fatalf("user creation must be conditional on the user being absent: %q", first)
	}
	if strings.HasPrefix(strings.TrimSpace(first), "useradd") {
		t.Fatalf("useradd must never run unconditionally: %q", first)
	}
}

// Every step whose work is to install something carries its own
// convergence: the resume contract is that what a previous apply
// finished stays finished, and the engine can only keep that
// promise for steps that know how to say "already in place".
// The docker repo file is written only by the engine step, which
// runs after the key step; the key step starts by moving a
// keyless repo file aside. A repo file without its key poisons
// every later apt refresh — apt refuses to update past a repository
// it cannot verify — and the failure looks like the box's network.
// Every narrated step's notes ride beside its commands, one line
// each: the loading page tells the story while mymo works.
func TestNotesRideBesideTheirCommands(t *testing.T) {
	r := ubuntuRunner()
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, s := range steps {
		if len(s.Notes) == 0 {
			continue
		}
		if len(s.Notes) != len(s.Exec) {
			t.Errorf("step %q has %d notes for %d commands", s.Control, len(s.Notes), len(s.Exec))
		}
		for _, n := range s.Notes {
			if n == "" {
				t.Errorf("step %q has an empty note", s.Control)
			}
		}
	}
}

func TestARepoFileNeverExistsWithoutItsKey(t *testing.T) {
	r := ubuntuRunner()
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, s := range steps {
		// the repo steps carry no guarded files: those all land
		// before the exec begins, and a repo file must land only
		// after its key
		if s.Control == "docker" || s.Control == "caddy" {
			for _, f := range s.Files {
				if strings.Contains(f.Path, "sources.list.d") {
					t.Errorf("%s still writes its repo file as a guarded file, before its key: %s", s.Control, f.Path)
				}
			}
		}
		if s.Control == "docker" {
			if !strings.Contains(strings.Join(s.Exec[0], " "), "mv /etc/apt/sources.list.d/docker.list") {
				t.Errorf("the docker step does not unpoison a keyless repo file first:\n%s", strings.Join(s.Exec[0], " "))
			}
			keyAt, fileAt := -1, -1
			for i, argv := range s.Exec {
				j := strings.Join(argv, " ")
				if strings.Contains(j, "/gpg -o /etc/apt/keyrings/docker.asc") {
					keyAt = i
				}
				if strings.Contains(j, "sources.list.d/docker.list") && i > 0 {
					if fileAt < 0 {
						fileAt = i
					}
				}
			}
			if keyAt < 0 || fileAt < 0 || fileAt < keyAt {
				t.Errorf("the repo file (cmd %d) does not follow its key (cmd %d)", fileAt, keyAt)
			}
		}
		if s.Control == "caddy" {
			// caddy carries no apt repository at all: the rule holds
			// trivially, and the cloudsmith leftover is moved aside
			// wherever it came from — it 402s every apt on the box
			for _, argv := range s.Exec {
				if strings.Contains(strings.Join(argv, " "), "sources.list.d/caddy-stable.list") &&
					!strings.Contains(strings.Join(argv, " "), "mv ") {
					t.Errorf("the caddy step writes a repo file instead of moving it aside: %v", argv)
				}
			}
		}
	}
}

func TestInstallStepsCarryTheirConvergence(t *testing.T) {
	r := ubuntuRunner()
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit(), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	have := map[string][]string{}
	for _, s := range steps {
		have[s.Control] = s.Check
	}
	for _, control := range []string{"unattended-upgrades", "firewall", "docker", "caddy"} {
		if len(have[control]) == 0 {
			t.Errorf("step %q installs a package but cannot say when it is already in place", control)
		}
	}
	// the firewall's converged state is active, not merely installed
	if !strings.Contains(strings.Join(have["firewall"], " "), "Status: active") {
		t.Errorf("the firewall converges on installed, not active: %q", strings.Join(have["firewall"], " "))
	}
}

// Auto-updates converge on the package AND the switch that makes it
// run: on a minimal Debian the package alone ships dormant, and
// "installed" would be a quiet box that never updates itself.
func TestSecurityUpdatesConvergeOnTheSwitch(t *testing.T) {
	steps, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), cleanSnapshot(), cleanAudit(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Control != "unattended-upgrades" {
			continue
		}
		if len(s.Files) != 1 || s.Files[0].Path != baseline.AutoUpgradesFile {
			t.Fatalf("the step does not pin %s: %+v", baseline.AutoUpgradesFile, s.Files)
		}
		if !strings.Contains(s.Files[0].Content, `APT::Periodic::Unattended-Upgrade "1"`) {
			t.Errorf("the pinned policy does not turn unattended upgrades on:\n%s", s.Files[0].Content)
		}
		joined := strings.Join(s.Check, " ")
		if !strings.Contains(joined, "unattended-upgrades") || !strings.Contains(joined, baseline.AutoUpgradesFile) {
			t.Errorf("the convergence probe must cover the package and the switch: %s", joined)
		}
		return
	}
	t.Fatal("no unattended-upgrades step in the plan")
}
