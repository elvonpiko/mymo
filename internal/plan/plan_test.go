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
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit())
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
	if !strings.Contains(docker.Files[0].Content, "https://download.docker.com/linux/ubuntu noble stable") ||
		!strings.Contains(docker.Files[0].Content, "arch=amd64") {
		t.Errorf("docker source list wrong: %q", docker.Files[0].Content)
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
	if !strings.Contains(caddy.Files[0].Content, "https://dl.cloudsmith.io/public/caddy/stable/deb/debian any main") {
		t.Errorf("caddy source list wrong: %q", caddy.Files[0].Content)
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
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit()); err != nil {
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

func TestGenerateBlockedByDecisions(t *testing.T) {
	audit := cleanAudit()
	audit.Listeners = map[int]string{80: "nginx"}
	audit.DockerPkg = "docker.io"
	_, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), cleanSnapshot(), audit)

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
	_, err := Generate(context.Background(), ubuntuRunner(), n, cleanSnapshot(), cleanAudit())
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
	steps, err := Generate(context.Background(), ubuntuRunner(), n, snap, cleanAudit())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var ssh Step
	for _, s := range steps {
		if s.Control == "ssh-hardening" {
			ssh = s
		}
	}
	if !strings.Contains(ssh.Detail, "disable root ssh") {
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

	steps, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), snap, audit)
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
	steps, err := Generate(context.Background(), ubuntuRunner(), cleanNode(), cleanSnapshot(), audit)
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
	steps, err := Generate(context.Background(), r, cleanNode(), snap, cleanAudit())
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

func TestGenerateRefusesUnknownCodename(t *testing.T) {
	r := ubuntuRunner()
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = ""
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit()); err == nil {
		t.Fatal("missing codename accepted")
	}
	r.responses[`sh -c . /etc/os-release && echo "$ID $VERSION_CODENAME"`] = "alpine edge"
	if _, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), cleanAudit()); err == nil {
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
	steps, err := Generate(context.Background(), r, cleanNode(), cleanSnapshot(), a)
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
