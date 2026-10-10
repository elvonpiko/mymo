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
	// Notes rides beside Exec, one plain line per command: the
	// loading page says what mymo is doing while the command runs.
	// A step may leave Notes empty; the command's own name is then
	// the narration.
	Notes []string
	Gate  string
	// Check is the step's end state, probed as one command: exit 0
	// means the exec would be a no-op with a cost — an apt refresh,
	// a download — that a resumed box may not be able to pay again,
	// so the engine skips the exec and reports the step KEPT. A
	// non-zero exit means the work stands: the exec runs as planned.
	Check []string
}

// repoFileWrite places one pinned repository line through the
// command stream — not the guarded files, which all land before the
// exec begins. The rule has teeth (ADR-0009): a repo file never
// exists without its key, so the file is written here, after the
// key. An existing file that differs is moved aside first; mymo
// never silently overwrites what it did not write.
func repoFileWrite(path, line string) []string {
	return []string{"sh", "-c", fmt.Sprintf(
		"printf '%%s\n' '%s' | cmp -s - %s || { mv %s %s.pre-mymo 2>/dev/null || true; printf '%%s\n' '%s' > %s; }",
		line, path, path, path, line, path)}
}

// installedCheck is the converged probe for a step whose whole exec
// exists to put one package in place: dpkg says install ok installed
// or it does not. The quoting is dpkg's own format syntax — the
// argument travels verbatim to the shell, which is the only
// interpreter dpkg's ${Status} placeholder ever had.
func installedCheck(pkg string) []string {
	return []string{"sh", "-c", "dpkg-query -W -f='${Status}' " + pkg + " 2>/dev/null | grep -q 'install ok installed'"}
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
		// the step's whole work is the package: installed is
		// converged, and a resume never refreshes apt for it again
		Check: installedCheck("unattended-upgrades"),
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
	// the converged state is active, not merely installed: a step
	// interrupted between install and enable re-runs to its end
	step.Check = []string{"sh", "-c", "ufw status 2>/dev/null | grep -q 'Status: active'"}
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

// dockerStep installs the engine from docker's official
// repository, in the order the rule demands (ADR-0009): the signing
// key lands before the repo file exists, because apt refuses to
// refresh past a repository it cannot verify — and a keyless repo
// file left by an interrupted attempt poisons every later refresh,
// reading like the box's broken network. The loading page narrates
// each command, so one step is one story on the screen.
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
	line := fmt.Sprintf("deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] %s %s stable", arch, repo, codename)
	return Step{
		Control:  "docker",
		Title:    "docker engine",
		Detail:   "install docker-ce + plugins from docker's repo",
		Packages: baseline.DockerPackages,
		// the belt for a resume that finds the engine in place with
		// the marker never written
		Check: installedCheck("docker-ce"),
		Exec: [][]string{
			// an interrupted attempt may have left its repo file
			// behind without the key — moved aside before anything
			// touches apt; inert to apt either way
			{"sh", "-c", "test -r /etc/apt/keyrings/docker.asc || mv /etc/apt/sources.list.d/docker.list /etc/apt/sources.list.d/docker.list.interrupted 2>/dev/null || true"},
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "ca-certificates", "curl"},
			{"install", "-m", "0755", "-d", "/etc/apt/keyrings"},
			{"curl", "-fsSL", repo + "/gpg", "-o", "/etc/apt/keyrings/docker.asc"},
			{"chmod", "a+r", "/etc/apt/keyrings/docker.asc"},
			repoFileWrite("/etc/apt/sources.list.d/docker.list", line),
			{"apt-get", "update"},
			append([]string{"apt-get", "install", "-y"}, baseline.DockerPackages...),
		},
		Notes: []string{
			"moving any keyless leftover repo file aside",
			"refreshing apt before anything lands",
			"installing ca-certificates + curl",
			"making the keyring directory",
			"fetching docker's signing key",
			"making the key readable",
			"adding docker's repository",
			"refreshing apt with docker's repo",
			"installing docker-ce + plugins",
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

// caddyCloudsmithAside moves the cloudsmith apt repository out of
// apt's way, keeping one copy: the repository is sponsor-quota
// limited and has served 402 Payment Required to every apt on the
// box, and mymo does not use it. Whatever put the file there — a
// person, an earlier mymo — the box's apt does not work while it
// stands.
const caddyCloudsmithAside = "rm -f /etc/apt/sources.list.d/caddy-stable.list.pre-mymo 2>/dev/null; " +
	"mv /etc/apt/sources.list.d/caddy-stable.list /etc/apt/sources.list.d/caddy-stable.list.pre-mymo 2>/dev/null || true"

// caddyStep installs caddy by its official production recipe: the
// static binary from its own release distribution, verified by the
// release's own SHA-512 digest, run under its official systemd unit.
// No third-party apt repository — the cloudsmith one is
// sponsor-quota limited and was observed refusing every apt on the
// box with 402 Payment Required. A version pinned by digest is
// deterministic; a rolling repo is not.
func caddyStep(f facts.Node, a preflight.Audit) Step {
	if caddyAdoptable(f, a) {
		return Step{
			Control: "caddy",
			Title:   "caddy \u2014 adopt",
			Detail:  "adopt caddy " + firstWord(f.Caddy) + "; nothing reinstalls",
			Exec: [][]string{
				{"sh", "-c", caddyCloudsmithAside},
				{"install", "-d", "/etc/caddy/mymo"},
				{"sh", "-c", "test -f /etc/caddy/mymo/Placeholder.caddy || printf '# mymo routes land here as apps deploy\n' > /etc/caddy/mymo/Placeholder.caddy"},
				{"sh", "-c", "grep -q '" + baseline.CaddyImport + "' /etc/caddy/Caddyfile 2>/dev/null || printf '" + baseline.CaddyImport + "\n' >> /etc/caddy/Caddyfile"},
				{"systemctl", "enable", "--now", "caddy"},
				{"systemctl", "reload", "caddy"},
			},
			Notes: []string{
				"moving the cloudsmith repo aside \u2014 it breaks apt",
				"making mymo's route directory",
				"planting the routes placeholder",
				"adding mymo's import to the Caddyfile",
				"enabling caddy",
				"reloading caddy with mymo's import",
			},
		}
	}
	asset, ok := baseline.CaddyAssets[f.Arch]
	if !ok {
		// preflight refuses unsupported architectures before a plan
		// is ever generated; this arm keeps the step honest if it is
		// ever reached anyway
		asset, ok = baseline.CaddyAssets["x86_64"]
		if !ok {
			asset = baseline.CaddyAsset{}
		}
	}
	url := baseline.CaddyReleaseBase + asset.File
	return Step{
		Control:  "caddy",
		Title:    "caddy",
		Detail:   "install caddy " + baseline.CaddyVersion + " \u2014 official pinned binary",
		Packages: []string{baseline.CaddyPackage},
		// the end state is the binary in place and mymo's marker in
		// the Caddyfile: both present and the step is done, however
		// many attempts it took
		Check: []string{"sh", "-c", "test -x /usr/bin/caddy && grep -q '" + baseline.CaddyImport + "' /etc/caddy/Caddyfile 2>/dev/null"},
		Files: []File{
			// the official systemd unit, byte for byte from
			// caddy's own dist: mymo runs caddy the way its makers
			// run it
			{Path: "/etc/systemd/system/caddy.service",
				Content: baseline.CaddyServiceUnit, Mode: "0644", Owner: "root:root"},
		},
		Exec: [][]string{
			{"sh", "-c", caddyCloudsmithAside},
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "ca-certificates", "curl", "tar"},
			{"sh", "-c", "getent group caddy >/dev/null || groupadd --system caddy"},
			{"useradd", "--system", "--gid", "caddy", "--create-home", "--home-dir", "/var/lib/caddy", "--shell", "/usr/sbin/nologin", "--comment", "Caddy web server", "caddy"},
			{"curl", "-fsSL", url, "-o", "/tmp/caddy.tar.gz"},
			{"sh", "-c", "echo '" + asset.SHA512 + "  /tmp/caddy.tar.gz' | sha512sum -c -"},
			{"sh", "-c", "tar -xzf /tmp/caddy.tar.gz -C /tmp caddy && install -m 0755 /tmp/caddy /usr/bin/caddy && rm -f /tmp/caddy /tmp/caddy.tar.gz"},
			{"systemctl", "daemon-reload"},
			{"systemctl", "enable", "--now", "caddy"},
			{"install", "-d", "/etc/caddy/mymo"},
			{"sh", "-c", "test -f /etc/caddy/mymo/Placeholder.caddy || printf '# mymo routes land here as apps deploy\n' > /etc/caddy/mymo/Placeholder.caddy"},
			{"sh", "-c", "grep -q '" + baseline.CaddyImport + "' /etc/caddy/Caddyfile 2>/dev/null || printf '" + baseline.CaddyImport + "\n' >> /etc/caddy/Caddyfile"},
			{"systemctl", "reload", "caddy"},
		},
		Notes: []string{
			"moving the cloudsmith repo aside \u2014 it breaks apt",
			"refreshing apt before the prerequisites",
			"installing ca-certificates, curl, tar",
			"creating the caddy group",
			"creating the caddy service user",
			"downloading caddy " + baseline.CaddyVersion + " for " + f.Arch,
			"verifying the release checksum",
			"installing the caddy binary to /usr/bin",
			"loading caddy's systemd unit",
			"enabling caddy",
			"making mymo's route directory",
			"planting the routes placeholder",
			"adding mymo's import to the Caddyfile",
			"reloading caddy with mymo's import",
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
