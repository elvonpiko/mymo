// Package plan turns the pinned baseline and a node's preflight
// audit into the concrete change list a human reviews before
// anything runs. Generation is read-only: it captures the
// distribution codename and emits steps; every command the steps
// carry executes later, at apply, behind the confirmation gate.
//
// The rules that shape every plan:
//
//   - nothing is planned when the audit demands a decision or aborts;
//     "when in doubt, stop" starts here, not at apply
//   - adopt beats reinstall: components preflight cleared are taken
//     over as-is, and their existing configuration is never rewritten
//   - the sshd step is gated: the reload happens only after key
//     access through the new mymo account is proven on a second
//     connection
//   - every file a step writes is stated in full in the plan, so the
//     review sees exactly what the node will carry
package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// File is one file a step writes on the node.
type File struct {
	Path    string
	Content string
	Mode    string // octal string, e.g. "0644"
	Owner   string // user:group
}

// Step is one reviewed action. Files are written (validated, then
// placed), then Packages installed, then Exec run in order — apply
// owns that sequence and its rollback; the plan owns the content.
type Step struct {
	Control  string
	Title    string
	Detail   string
	Files    []File
	Packages []string
	Exec     [][]string
	Gate     string
}

// BlockedError reports the decisions standing between a node and its
// plan. Planning never proceeds past them.
type BlockedError struct {
	Decisions []preflight.Check
}

func (e *BlockedError) Error() string {
	titles := make([]string, 0, len(e.Decisions))
	for _, c := range e.Decisions {
		titles = append(titles, c.Title)
	}
	if len(titles) == 1 {
		return "1 decision before planning: " + titles[0]
	}
	return fmt.Sprintf("%d decisions before planning: %s", len(titles), strings.Join(titles, ", "))
}

// Generate builds the change list for one node from its fresh
// snapshot and deep audit. It refuses to plan anything when the
// audit's verdict is decide or abort, listing exactly what stands in
// the way.
func Generate(ctx context.Context, r preflight.Runner, n domain.Node, f facts.Node, a preflight.Audit) ([]Step, error) {
	checks := preflight.Evaluate(f, a)
	if worst := preflight.Verdict(checks); worst >= preflight.Decide {
		var decisions []preflight.Check
		for _, c := range checks {
			if c.Outcome >= preflight.Decide {
				decisions = append(decisions, c)
			}
		}
		return nil, &BlockedError{Decisions: decisions}
	}

	id, codename, err := captureDistro(ctx, r)
	if err != nil {
		return nil, err
	}

	arch := baseline.KernelToPackageArch(f.Arch)
	if arch == "" {
		return nil, fmt.Errorf("architecture %q has no package mapping", f.Arch)
	}
	// a hostile account name must never reach an sshd file
	dropin, err := sshDropin(n.User)
	if err != nil {
		return nil, err
	}

	steps := []Step{
		mymoUserStep(),
		sshStep(f, dropin),
		sysctlStep(),
		updatesStep(),
		firewallStep(a),
		dockerStep(f, a, id, codename, arch),
		daemonStep(f, a),
		caddyStep(f, a),
		stateStep(),
	}
	return steps, nil
}

// captureDistro reads the distribution id and codename the official
// repositories key their paths on. It is the only thing generation
// runs on the node.
func captureDistro(ctx context.Context, r preflight.Runner) (id, codename string, err error) {
	out, _, err := r.Run(ctx, "sh", "-c", `. /etc/os-release && echo "$ID $VERSION_CODENAME"`)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 || fields[1] == "" {
		return "", "", fmt.Errorf("cannot read the distribution codename from /etc/os-release")
	}
	switch fields[0] {
	case "ubuntu", "debian":
		return fields[0], fields[1], nil
	}
	return "", "", fmt.Errorf("unsupported distribution id %q", fields[0])
}

// sshGateSecondConnection is the one gate the whole lifecycle turns
// on: sshd's new posture loads only after the new key works.
const sshGateSecondConnection = "the new key is proven on a fresh connection before sshd reloads — your access is untouched"

func mymoUserStep() Step {
	return Step{
		Control: "mymo-user",
		Title:   "mymo admin user",
		Detail:  "create mymo user with its key and sudo rule",
		Files: []File{
			{Path: baseline.MymoSudoersDropin, Content: baseline.MymoSudoersRule + "\n", Mode: "0440", Owner: "root:root"},
		},
		Exec: [][]string{
			// a resume finds the user already there; useradd alone
			// would refuse and break the second run
			{"sh", "-c", "id " + baseline.MymoUser + " >/dev/null 2>&1 || useradd -m -s /bin/bash " + baseline.MymoUser},
			{"install", "-d", "-m", "0700", "-o", baseline.MymoUser, "-g", baseline.MymoUser, "/home/" + baseline.MymoUser + "/.ssh"},
			// the key pair is generated locally and the public key
			// lands here at apply; the placeholder is replaced with
			// the real key material, never a secret the plan could
			// leak
			{"sh", "-c", "printf '%s\\n' '<mymo-public-key>' > /home/" + baseline.MymoUser + "/.ssh/authorized_keys"},
			{"chown", baseline.MymoUser + ":" + baseline.MymoUser, "/home/" + baseline.MymoUser + "/.ssh/authorized_keys"},
			{"chmod", "0600", "/home/" + baseline.MymoUser + "/.ssh/authorized_keys"},
			{"visudo", "-cf", baseline.MymoSudoersDropin},
		},
	}
}

func sshStep(f facts.Node, dropin string) Step {

	detail := "harden sshd: no root, no passwords, in a drop-in"
	if f.User == "root" {
		detail = "no root ssh; access continues as mymo + console"
	}
	return Step{
		Control: "ssh-hardening",
		Title:   "sshd hardening",
		Detail:  detail,
		Files: []File{
			{Path: baseline.SSHDropin, Content: dropin, Mode: "0644", Owner: "root:root"},
		},
		Exec: [][]string{
			{"sshd", "-t"},
			{"systemctl", "reload", "ssh"},
		},
		Gate: sshGateSecondConnection,
	}
}

// sshDropin renders the pinned directives plus the forwarding
// carve-out into the exact file sshd will load.
func sshDropin(operator string) (string, error) {
	var b strings.Builder
	for _, d := range baseline.SSHDirectives {
		fmt.Fprintf(&b, "%s %s\n", d.Key, d.Value)
	}
	carve, err := baseline.SSHForwardingCarveOut(operator)
	if err != nil {
		return "", err
	}
	if len(carve) > 0 {
		b.WriteString("\n")
		for _, line := range carve {
			b.WriteString(line + "\n")
		}
	}
	return b.String(), nil
}

func sysctlStep() Step {
	keys := make([]string, 0, len(baseline.Sysctls))
	for k := range baseline.Sysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# mymo baseline " + baseline.Version + " — tested sysctl floor\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, baseline.Sysctls[k])
	}
	return Step{
		Control: "sysctl",
		Title:   "kernel hardening",
		Detail:  "pin the tested sysctl floor via /etc/sysctl.d",
		Files: []File{
			{Path: "/etc/sysctl.d/60-mymo.conf", Content: b.String(), Mode: "0644", Owner: "root:root"},
		},
		Exec: [][]string{{"sysctl", "--system"}},
	}
}

func updatesStep() Step {
	return Step{
		Control:  "unattended-upgrades",
		Title:    "security updates",
		Detail:   "auto-apply security patches; reboots stay yours",
		Packages: baseline.SystemPackages,
		Exec: [][]string{
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "unattended-upgrades"},
		},
	}
}

func firewallStep(a preflight.Audit) Step {
	step := Step{
		Control:  "firewall",
		Title:    "firewall",
		Packages: []string{"ufw"},
	}
	if a.Firewall == "active" {
		step.Detail = "add rules to the active ufw; its defaults are kept"
		step.Exec = [][]string{
			{"ufw", "limit", "22/tcp"},
			{"ufw", "allow", "80/tcp"},
			{"ufw", "allow", "443/tcp"},
		}
		return step
	}
	step.Detail = "install ufw \u2014 deny incoming, limit ssh, open 80/443"
	step.Exec = [][]string{
		{"apt-get", "update"},
		{"apt-get", "install", "-y", "ufw"},
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
		{"ufw", "limit", "22/tcp"},
		{"ufw", "allow", "80/tcp"},
		{"ufw", "allow", "443/tcp"},
		{"ufw", "--force", "enable"},
	}
	return step
}

// dockerAdoptable reports whether an existing docker can be taken
// over: preflight already refused a foreign daemon configuration, so
// a clean adopt here is honest.
func dockerAdoptable(f facts.Node, a preflight.Audit) bool {
	if a.DockerDaemonCfg != "" {
		return false // preflight made this a decision; unreachable here
	}
	return a.DockerPkg == "docker-ce" || f.Docker != ""
}

func dockerStep(f facts.Node, a preflight.Audit, id, codename, arch string) Step {
	if dockerAdoptable(f, a) {
		return Step{
			Control: "docker",
			Title:   "docker \u2014 adopt",
			Detail:  "adopt the installed docker; nothing reinstalls",
			Exec:    [][]string{{"systemctl", "enable", "docker"}},
		}
	}
	repo := fmt.Sprintf("https://download.docker.com/linux/%s", id)
	return Step{
		Control: "docker",
		Title:   "docker engine",
		Detail:  "install docker-ce + plugins from docker's repo",
		Files: []File{
			{Path: "/etc/apt/sources.list.d/docker.list",
				Content: fmt.Sprintf("deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] %s %s stable\n", arch, repo, codename),
				Mode:    "0644", Owner: "root:root"},
		},
		Packages: baseline.DockerPackages,
		Exec: [][]string{
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "ca-certificates", "curl"},
			{"install", "-m", "0755", "-d", "/etc/apt/keyrings"},
			{"curl", "-fsSL", repo + "/gpg", "-o", "/etc/apt/keyrings/docker.asc"},
			{"chmod", "a+r", "/etc/apt/keyrings/docker.asc"},
			{"apt-get", "update"},
			append([]string{"apt-get", "install", "-y"}, baseline.DockerPackages...),
		},
	}
}

func daemonStep(f facts.Node, a preflight.Audit) Step {
	step := Step{
		Control: "docker-daemon",
		Title:   "docker daemon",
		Files: []File{
			{Path: "/etc/docker/daemon.json", Content: baseline.DockerDaemonConfig + "\n", Mode: "0644", Owner: "root:root"},
		},
	}
	if dockerAdoptable(f, a) {
		step.Detail = "apply daemon policy; restart re-runs containers once"
		step.Exec = [][]string{{"systemctl", "restart", "docker"}}
		return step
	}
	step.Detail = "bound logs + live-restore; docker starts enabled"
	step.Exec = [][]string{{"systemctl", "enable", "--now", "docker"}}
	return step
}

// caddyAdoptable mirrors dockerAdoptable: preflight refuses an
// existing Caddyfile, so adoption here is always clean.
func caddyAdoptable(f facts.Node, a preflight.Audit) bool {
	if a.CaddyConfig != "" {
		return false
	}
	return f.Caddy != ""
}

func caddyStep(f facts.Node, a preflight.Audit) Step {
	if caddyAdoptable(f, a) {
		return Step{
			Control: "caddy",
			Title:   "caddy \u2014 adopt",
			Detail:  "adopt caddy " + firstWord(f.Caddy) + "; its config is untouched",
			Exec:    [][]string{{"systemctl", "enable", "caddy"}},
		}
	}
	return Step{
		Control: "caddy",
		Title:   "caddy",
		Detail:  "install caddy from the official cloudsmith repo",
		Files: []File{
			{Path: "/etc/apt/sources.list.d/caddy-stable.list",
				Content: "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any main\n",
				Mode:    "0644", Owner: "root:root"},
		},
		Packages: []string{baseline.CaddyPackage},
		Exec: [][]string{
			{"install", "-d", "/usr/share/keyrings"},
			{"sh", "-c", "curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg"},
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "caddy"},
			{"systemctl", "enable", "--now", "caddy"},
		},
	}
}

func stateStep() Step {
	ids := make([]string, 0, len(baseline.Controls))
	for _, c := range baseline.Controls {
		ids = append(ids, fmt.Sprintf("%q", c.ID))
	}
	marker := fmt.Sprintf("{\n  \"baseline\": %q,\n  \"appliedAt\": \"<at apply>\",\n  \"controls\": [%s]\n}\n",
		baseline.Version, strings.Join(ids, ", "))
	return Step{
		Control: "state-dir",
		Title:   "mymo state",
		Detail:  "state dir + baseline marker for drift detection",
		Files: []File{
			{Path: baseline.StateDir + "/baseline.json", Content: marker, Mode: "0644", Owner: baseline.MymoUser + ":" + baseline.MymoUser},
		},
		Exec: [][]string{
			{"install", "-d", "-m", "0750", "-o", baseline.MymoUser, "-g", baseline.MymoUser, baseline.StateDir},
		},
	}
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
