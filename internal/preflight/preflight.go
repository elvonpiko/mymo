// Package preflight implements mymo's App Host preflight: a deep,
// strictly read-only audit of everything the baseline will touch,
// evaluated into per-check verdicts. Preflight never mutates; its job
// is to say one of four things per the spec — safe to continue, safe
// to adopt, requires a user decision, or unsupported — and to stop
// when in doubt.
package preflight

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/facts"
)

// scanTimeout bounds a whole preflight scan when the caller sets no
// deadline.
const scanTimeout = 90 * time.Second

// Runner executes one explicit-argument-vector command on a node;
// *ssh.Client satisfies it, as does facts' canned runner in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, int, error)
}

// Outcome is a check's verdict, ordered by severity.
type Outcome int

const (
	// Pass means the baseline's assumption holds or its install has
	// a free path.
	Pass Outcome = iota
	// Adopt means something already exists that mymo can take over
	// as-is.
	Adopt
	// Decide means the user must make a call mymo refuses to make.
	Decide
	// Abort means the baseline cannot proceed on this node.
	Abort
)

// String renders the outcome word per the spec's language.
func (o Outcome) String() string {
	switch o {
	case Pass:
		return "ok"
	case Adopt:
		return "adopt"
	case Decide:
		return "decide"
	default:
		return "abort"
	}
}

// Check is one audited fact and its verdict.
type Check struct {
	Group   string
	Title   string
	Outcome Outcome
	Detail  string
	// Fix is the explicit way out of a finding that stands between
	// the box and the plan: what to do, on the box, to let mymo
	// take it from there. Empty when the outcome needs nothing.
	Fix string
}

// Audit is the deep read-only scan result. Zero values mean unknown,
// never "absent" — preflight names what it could not see.
type Audit struct {
	// DockerDaemonCfg is a foreign /etc/docker/daemon.json: a policy
	// that is not mymo's own bytes, which mymo will not overwrite
	// without review. A file that matches mymo's policy exactly is
	// not foreign — DockerDaemonOurs says so.
	DockerDaemonCfg string
	// DockerDaemonOurs reports a daemon.json that is exactly mymo's
	// own policy — written by a previous apply or aligned by hand.
	DockerDaemonOurs bool

	// CaddyConfig is a foreign /etc/caddy/Caddyfile — one without
	// mymo's import line. A Caddyfile that carries the import is
	// mymo's own to manage; CaddyOurs says so.
	CaddyConfig string
	// CaddyOurs reports a Caddyfile that already imports mymo's
	// routes: mymo's marker for its own caddy.
	CaddyOurs bool

	UID  int
	Sudo bool // passwordless sudo confirmed

	// SSH directives seen in sshd_config and its includes: each maps
	// a directive to every value found. Conflicting values are
	// reported; the effective value is only proven at apply time with
	// sshd -T, which needs root.
	SSHDirectives map[string][]string
	// SSHOurs holds the directives from mymo's current drop-in home;
	// SSHOursOld from its retired home. Values there are mymo's own
	// work: recognized, never treated as a foreign conflict.
	SSHOurs    map[string][]string
	SSHOursOld map[string][]string

	Firewall string // "active", "inactive", "absent", "unknown"

	// Listeners maps a listening port to the owning process name
	// ("" when the owner could not be seen without root).
	Listeners map[int]string

	// Proxies are conflicting proxy binaries found on disk, even if
	// they are not running.
	Proxies []string

	DockerPkg    string // "docker-ce", "docker.io", or "" when absent
	Containers   int    // -1 when the daemon state could not be read
	MymoUserUsed bool
	MymoDirUsed  bool

	// MymoSudoers is the content of mymo's sudoers drop-in, "" when
	// absent — the fingerprint that separates mymo's own user from a
	// foreign one that happens to share the name.
	MymoSudoers string
	// MymoMarker is the raw baseline marker a previous apply
	// recorded, "" when absent — the drift surface.
	MymoMarker string
}

// scanned at most: preflight keeps its commands to the handful the
// baseline actually touches — no busywork round trips.
func Scan(ctx context.Context, r Runner) (Audit, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, scanTimeout)
		defer cancel()
	}

	var s Audit
	if out, _, err := r.Run(ctx, "id", "-u"); err != nil {
		return Audit{}, err
	} else {
		s.UID, _ = strconv.Atoi(strings.TrimSpace(firstLine(out)))
	}
	if _, code, err := r.Run(ctx, "sudo", "-n", "true"); err != nil {
		return Audit{}, err
	} else {
		s.Sudo = code == 0
	}

	// SSH configuration: the main file plus any includes the
	// distribution ships, with mymo's own drop-in homes read
	// separately — a conflict between a foreign file and mymo's own
	// work is not the operator's to resolve, and the audit must be
	// able to tell whose is whose. Nonzero exits mean unreadable or
	// absent; directives stay unknown, never guessed.
	s.SSHDirectives = map[string][]string{}
	s.SSHOurs = map[string][]string{}
	s.SSHOursOld = map[string][]string{}
	foreignSSH := "for f in /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf; do " +
		"case \"$f\" in " + baseline.SSHDropin + "|" + baseline.SSHRetiredDropin + ") continue;; esac; " +
		"cat \"$f\" 2>/dev/null || true; done"
	out, _, err := r.Run(ctx, "sh", "-c", foreignSSH)
	if err != nil {
		return Audit{}, err
	}
	collectSSHDirectives(out, s.SSHDirectives)
	// mymo's own homes: absent is fine — that is a box mymo has not
	// hardened yet, and the plan says so in its own words
	if out, _, err := r.Run(ctx, "cat", baseline.SSHDropin); err == nil {
		collectSSHDirectives(out, s.SSHOurs)
	}
	if out, _, err := r.Run(ctx, "cat", baseline.SSHRetiredDropin); err == nil {
		collectSSHDirectives(out, s.SSHOursOld)
	}

	// Firewall: ufw status needs root; without privilege mymo says
	// unknown rather than assuming it is absent.
	switch out, code, err := r.Run(ctx, "sudo", "-n", "ufw", "status"); {
	case err != nil:
		return Audit{}, err
	case code == 0:
		s.Firewall = "inactive"
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "Status: active") {
				s.Firewall = "active"
			}
		}
	case s.Sudo:
		s.Firewall = "absent" // privilege held, ufw not installed
	default:
		s.Firewall = "unknown"
	}

	// Listeners on the ports Caddy wants, with owners when visible:
	// the privileged listing first, the unprivileged one whenever
	// sudo is refused or fails.
	s.Listeners = map[int]string{}
	if out, code, err := r.Run(ctx, "sudo", "-n", "ss", "-tlnp"); err == nil && code == 0 {
		parseListeners(out, s.Listeners, true)
	} else if out, _, err := r.Run(ctx, "ss", "-tln"); err != nil {
		return Audit{}, err
	} else {
		parseListeners(out, s.Listeners, false)
	}

	// Proxy binaries that conflict with the Caddy-only model.
	if out, _, err := r.Run(ctx, "sh", "-c",
		"command -v "+strings.Join(baseline.ConflictingProxies, " ")+" 2>/dev/null"); err != nil {
		return Audit{}, err
	} else {
		s.Proxies = parseProxies(out, baseline.ConflictingProxies)
	}

	// Which Docker package, if any: official docker-ce or the
	// distribution docker.io the baseline refuses to adopt silently.
	if out, _, err := r.Run(ctx, "sh", "-c",
		`dpkg -l docker-ce docker.io 2>/dev/null | grep "^ii"`); err != nil {
		return Audit{}, err
	} else {
		s.DockerPkg = parseDockerPkg(out)
	}

	// Running containers tell the adopt story.
	if s.DockerPkg != "" && (s.Sudo || s.UID == 0) {
		if out, _, err := r.Run(ctx, "sh", "-c",
			"docker ps -q 2>/dev/null | wc -l"); err != nil {
			return Audit{}, err
		} else if n, perr := strconv.Atoi(strings.TrimSpace(firstLine(out))); perr == nil {
			s.Containers = n
		} else {
			s.Containers = -1
		}
	} else if s.DockerPkg != "" {
		s.Containers = -1
	}

	// Existing configuration mymo would touch: a foreign docker
	// daemon policy or Caddyfile is an adoption decision.
	if out, _, err := r.Run(ctx, "sh", "-c",
		"test -f /etc/docker/daemon.json && cat /etc/docker/daemon.json"); err != nil {
		return Audit{}, err
	} else if content := strings.TrimSpace(out); content != "" {
		// mymo's own policy is recognized by its exact bytes: what
		// mymo wrote — or what a person aligned to it — is not a
		// foreign policy, and the box is never asked to review
		// mymo's own work
		if content == strings.TrimSpace(baseline.DockerDaemonConfig) {
			s.DockerDaemonOurs = true
		} else {
			s.DockerDaemonCfg = content
		}
	}
	if out, _, err := r.Run(ctx, "sh", "-c",
		"test -f /etc/caddy/Caddyfile && cat /etc/caddy/Caddyfile"); err != nil {
		return Audit{}, err
	} else if content := strings.TrimSpace(out); content != "" {
		// the import line is mymo's marker in someone else's file
		// format: a Caddyfile that carries it is mymo's own caddy
		if strings.Contains(content, baseline.CaddyImport) {
			s.CaddyOurs = true
		} else {
			s.CaddyConfig = content
		}
	}

	// Existing mymo state from a previous or interrupted bootstrap.
	if out, _, err := r.Run(ctx, "sh", "-c",
		`id `+baseline.MymoUser+` 2>/dev/null; test -d `+baseline.StateDir+` && echo mymo-dir`); err != nil {
		return Audit{}, err
	} else {
		for _, line := range strings.Split(out, "\n") {
			switch {
			case strings.HasPrefix(line, "uid="):
				s.MymoUserUsed = true
			case strings.TrimSpace(line) == "mymo-dir":
				s.MymoDirUsed = true
			}
		}
	}

	// The sudoers drop-in and the marker are root-only files: the
	// probe reads them with whatever privilege the operator holds —
	// root directly, anyone else through the passwordless sudo the
	// audit has already confirmed. The exit code, not the output,
	// says whether the file is there: a refused read is an unknown,
	// never a "missing" rule that isn't.
	readPrivileged := func(path string) (string, bool) {
		argv := []string{"cat", path}
		if s.UID != 0 {
			argv = []string{"sudo", "-n", "cat", path}
		}
		out, code, err := r.Run(ctx, argv[0], argv[1:]...)
		if err != nil || code != 0 {
			return "", false
		}
		return strings.TrimSpace(out), true
	}
	// The sudoers drop-in is mymo's fingerprint: an existing mymo
	// user is only adoptable when the rule on the node is the rule
	// this baseline writes.
	if content, ok := readPrivileged(baseline.MymoSudoersDropin); ok {
		s.MymoSudoers = content
	}
	// The baseline marker a completed apply records: what ran, and
	// when — the drift surface.
	if content, ok := readPrivileged(baseline.StateDir + "/baseline.json"); ok {
		s.MymoMarker = content
	}

	return s, nil
}

// collectSSHDirectives records every value seen for the directives
// the baseline cares about; multiple values stay visible as a
// conflict instead of being silently resolved.
func collectSSHDirectives(out string, into map[string][]string) {
	wanted := map[string]bool{
		"passwordauthentication": true,
		"permitrootlogin":        true,
		"pubkeyauthentication":   true,
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, " ")
		if !found {
			key = line
			value = ""
		}
		key = strings.ToLower(key)
		if !wanted[key] {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(value))
		if !seen(into[key], v) {
			into[key] = append(into[key], v)
		}
	}
}

func seen(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// parseListeners records who listens on Caddy's ports. ss prints the
// local address as [host]:port; process names appear only with root.
func parseListeners(out string, into map[int]string, owners bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		port, ok := lastPort(fields[3])
		if !ok {
			continue
		}
		if _, tracked := into[port]; !tracked && !portWatched(port) {
			continue
		}
		owner := ""
		if owners {
			if i := strings.Index(line, "(\""); i >= 0 {
				if j := strings.Index(line[i+2:], "\","); j >= 0 {
					owner = line[i+2 : i+2+j]
				}
			}
		}
		if prev, exists := into[port]; !exists || (prev == "" && owner != "") {
			into[port] = owner
		}
	}
}

func portWatched(port int) bool {
	for _, p := range baseline.HTTPPorts {
		if p == port {
			return true
		}
	}
	return false
}

// lastPort reads the port from ss's local-address column, which may be
// "*:80", "0.0.0.0:443", "[::]:22", or a bare address without a port.
func lastPort(addr string) (int, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseProxies names the conflicting binaries that command -v found.
func parseProxies(out string, watched []string) []string {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		name := firstLine(line)
		if name == "" {
			continue
		}
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		for _, w := range watched {
			if name == w && !seen(found, name) {
				found = append(found, name)
				break
			}
		}
	}
	sort.Strings(found)
	return found
}

// parseDockerPkg reads dpkg output for the installed Docker package.
func parseDockerPkg(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "ii" {
			switch fields[1] {
			case "docker-ce", "docker.io":
				return fields[1]
			}
		}
	}
	return ""
}

// Evaluate turns a discovery snapshot plus the deep scan into the
// audit's verdicts, one check per baseline assumption. Order is
// display order: identity first, then privilege and capacity, then
// what the baseline installs, then conflicts.
func Evaluate(f facts.Node, s Audit) []Check {
	checks := make([]Check, 0, 10)

	// --- platform: os, arch, systemd as one verdict ---------------
	// the three read as one question — can the baseline's packages
	// and services run here — and the first failure answers it.
	switch {
	case f.OS == "" && f.Arch == "":
		checks = append(checks, Check{"platform", "platform", Abort, "unknown — observe the node first", "open the node once (mymo node open, or ssh) so facts can be read, then check again"})
	case f.OS == "":
		checks = append(checks, Check{"platform", "platform", Abort, "os unknown — observe the node first", "open the node once so facts can be read, then check again"})
	case !baseline.MatchesOS(f.OS):
		checks = append(checks, Check{"platform", "platform", Abort, f.OS + " — baseline " + baseline.Version + " supports " + baseline.SupportedOSShort, "rebuild the box on a supported release: " + baseline.SupportedOSShort})
	case f.Arch == "":
		checks = append(checks, Check{"platform", "platform", Abort, "architecture unknown — observe the node first", "open the node once so facts can be read, then check again"})
	case !baseline.ArchSupported(f.Arch):
		checks = append(checks, Check{"platform", "platform", Abort, f.Arch + " — no official Docker or Caddy packages", "use a box on an architecture with official packages (amd64, arm64, armhf)"})
	case !f.Systemd:
		checks = append(checks, Check{"platform", "platform", Abort, f.OS + " · " + f.Arch + " — but no systemd; the baseline manages services through it", "use a box with systemd — the baseline starts and supervises services through it"})
	default:
		checks = append(checks, Check{"platform", "platform", Pass, f.OS + " · " + f.Arch + " · systemd", ""})
	}

	// --- privilege ------------------------------------------------
	switch {
	case s.UID == 0:
		checks = append(checks, Check{"privilege", "privilege", Pass, "connected as root", ""})
	case s.Sudo:
		checks = append(checks, Check{"privilege", "privilege", Pass, "passwordless sudo as " + f.User, ""})
	default:
		checks = append(checks, Check{"privilege", "privilege", Abort, "neither root nor passwordless sudo — the baseline needs privilege to install and harden", "give this login passwordless sudo (visudo: " + f.User + " ALL=(ALL) NOPASSWD:ALL) or connect as root, then check again"})
	}

	// --- capacity -------------------------------------------------
	switch {
	case f.DiskTotal == 0:
		checks = append(checks, Check{"disk", "disk headroom", Abort, "root filesystem size unknown — observe the node first", "open the node once so facts can be read, then check again"})
	case f.DiskFree >= baseline.MinDiskFree:
		checks = append(checks, Check{"disk", "disk headroom", Pass, facts.FormatBytes(f.DiskFree) + " free of " + facts.FormatBytes(f.DiskTotal) + " — needs " + facts.FormatBytes(baseline.MinDiskFree), ""})
	default:
		checks = append(checks, Check{"disk", "disk headroom", Abort, "only " + facts.FormatBytes(f.DiskFree) + " free — needs " + facts.FormatBytes(baseline.MinDiskFree) + " for the baseline's installs", "grow the disk and its filesystem (your provider's resize, then growpart + resize2fs), then check again"})
	}

	switch {
	case f.MemTotal >= baseline.MinMemory:
		checks = append(checks, Check{"memory", "memory", Pass, facts.FormatBytes(f.MemTotal) + " total", ""})
	case f.MemTotal == 0:
		checks = append(checks, Check{"memory", "memory", Abort, "unknown — observe the node first", "open the node once so facts can be read, then check again"})
	default:
		checks = append(checks, Check{"memory", "memory", Decide, "only " + facts.FormatBytes(f.MemTotal) + " total — headroom for applications is thin", "resize to more memory, or carry on — mymo will set the box up, but apps will be tight"})
	}

	// --- what the baseline installs -------------------------------
	switch s.Firewall {
	case "active":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw is active — existing rules are kept; the baseline only adds its own", ""})
	case "inactive":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw is installed but inactive — the baseline enables it with its policy", ""})
	case "absent":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw not installed — the baseline installs and enables it", ""})
	default:
		checks = append(checks, Check{"firewall", "firewall", Decide, "firewall state unknown without root — the baseline will refuse to touch a firewall it cannot see", "rerun the check with a login that can sudo (or as root) — mymo will not touch a firewall it cannot see"})
	}

	switch s.DockerPkg {
	case "":
		if s.DockerDaemonCfg != "" {
			checks = append(checks, Check{"docker", "docker", Decide, "docker is present with its own daemon configuration — mymo applies its own policy; yours is not overwritten without review",
				"move it aside: sudo mv /etc/docker/daemon.json /etc/docker/daemon.json.pre-mymo — then check again"})
		} else if f.Docker != "" {
			checks = append(checks, Check{"docker", "docker", Adopt, "docker " + facts.DockerVersion(f.Docker) + " present but not from dpkg — mymo can adopt and manage it", ""})
		} else {
			checks = append(checks, Check{"docker", "docker", Pass, "not installed — the baseline installs docker-ce from the official repository", ""})
		}
	case "docker-ce":
		if s.DockerDaemonCfg != "" {
			checks = append(checks, Check{"docker", "docker", Decide, "docker-ce present with its own daemon configuration — mymo applies its own policy; yours is not overwritten without review",
				"move it aside: sudo mv /etc/docker/daemon.json /etc/docker/daemon.json.pre-mymo — then check again"})
			break
		}
		if s.DockerDaemonOurs {
			checks = append(checks, Check{"docker", "docker", Adopt, "official docker-ce " + facts.DockerVersion(f.Docker) + " with mymo's own daemon policy — kept as-is", ""})
			break
		}
		detail := "official docker-ce " + facts.DockerVersion(f.Docker) + " — mymo can adopt and manage it"
		if s.Containers > 0 {
			detail += ", " + strconv.Itoa(s.Containers) + " container(s) running"
		}
		checks = append(checks, Check{"docker", "docker", Adopt, detail, ""})
	case "docker.io":
		checks = append(checks, Check{"docker", "docker", Decide, "distribution docker.io — only you can replace it",
			"sudo apt-get remove -y docker.io — images and containers are not touched; the baseline installs docker-ce, then check again"})
	}

	switch {
	case f.Caddy != "" && s.CaddyConfig != "":
		checks = append(checks, Check{"caddy", "caddy", Decide, "caddy present with an existing Caddyfile — mymo manages caddy's configuration; adopting yours needs your review",
			"move it aside: sudo mv /etc/caddy/Caddyfile /etc/caddy/Caddyfile.pre-mymo — its sites stop serving; then check again"})
	case f.Caddy != "" && s.CaddyOurs:
		checks = append(checks, Check{"caddy", "caddy", Adopt, "caddy " + firstToken(f.Caddy) + " — its Caddyfile already imports mymo's routes", ""})
	case f.Caddy != "":
		checks = append(checks, Check{"caddy", "caddy", Adopt, "caddy " + firstToken(f.Caddy) + " present — mymo can adopt and manage it", ""})
	default:
		checks = append(checks, Check{"caddy", "caddy", Pass, "not installed — the baseline installs it", ""})
	}

	// --- conflicts: Caddy's ports as one verdict ------------------
	// both ports go to Caddy or the deal is off; one row states who
	// holds what without repeating the policy per port.
	{
		var parts []string
		outcome := Pass
		for _, port := range baseline.HTTPPorts {
			owner, taken := s.Listeners[port]
			switch {
			case !taken:
				parts = append(parts, strconv.Itoa(port)+" free")
			case owner == "caddy":
				parts = append(parts, strconv.Itoa(port)+" caddy")
				if outcome < Adopt {
					outcome = Adopt
				}
			case owner == "":
				parts = append(parts, strconv.Itoa(port)+" unknown holder")
				if outcome < Decide {
					outcome = Decide
				}
			default:
				parts = append(parts, strconv.Itoa(port)+" "+owner)
				if outcome < Decide {
					outcome = Decide
				}
			}
		}
		detail := strings.Join(parts, ", ")
		switch {
		case outcome == Pass:
			detail += " — both free for Caddy"
		case outcome == Adopt:
			detail += " — held by caddy, adopted with the caddy configuration"
		default:
			detail += " — Caddy is mymo's only proxy; free the ports or use a different host"
		}
		fix := ""
		if outcome == Decide {
			fix = "stop or disable whatever holds the ports above (systemctl stop/disable), or use a different host — then check again"
		}
		checks = append(checks, Check{"ports", "ports", outcome, detail, fix})
	}

	if len(s.Proxies) > 0 {
		checks = append(checks, Check{"proxy", "other proxies", Decide, "installed: " + strings.Join(s.Proxies, ", ") + " — mymo manages Caddy only",
			"remove the proxies listed above (sudo apt-get remove -y " + strings.Join(s.Proxies, " ") + "), or use a different host — then check again"})
	}

	// --- existing mymo state --------------------------------------
	// The sudoers drop-in is the fingerprint: mymo's own state is
	// adoptable — steps keep what exists — while anything that only
	// shares the name stays a decision.
	if s.MymoUserUsed || s.MymoDirUsed || s.MymoMarker != "" {
		rec, marked := facts.ParseBaselineMarker(s.MymoMarker)
		fingerprint := s.MymoSudoers == baseline.MymoSudoersRule
		switch {
		case marked && fingerprint && rec.Baseline == baseline.Version:
			checks = append(checks, Check{"mymo", "mymo state", Adopt,
				"mymo's own record, baseline " + rec.Baseline + " applied — steps keep what exists", ""})
		case marked && fingerprint:
			checks = append(checks, Check{"mymo", "mymo state", Decide,
				"baseline " + rec.Baseline + " is recorded; " + baseline.Version + " would supersede it — upgrades are explicit, never silent",
				"keep the mymo that recorded this baseline, or remove the node from mymo (mymo node remove) and set it up fresh"})
		case marked:
			checks = append(checks, Check{"mymo", "mymo state", Decide,
				"the marker is mine but the sudoers rule is not — the box's rules were edited; mymo refuses to guess who did what",
				"restore the rule: " + baseline.MymoSudoersRule + " in /etc/sudoers.d/" + baseline.MymoUser + " — or find who rewrote it, then check again"})
		case fingerprint:
			checks = append(checks, Check{"mymo", "mymo state", Adopt,
				"an interrupted mymo bootstrap — apply resumes where it stopped, keeping what exists", ""})
		default:
			what := make([]string, 0, 2)
			if s.MymoUserUsed {
				what = append(what, "user "+baseline.MymoUser+" exists without mymo's sudoers rule")
			}
			if s.MymoDirUsed {
				what = append(what, "state dir "+baseline.StateDir+" exists without a marker")
			}
			checks = append(checks, Check{"mymo", "mymo state", Decide,
				strings.Join(what, "; ") + " — mymo never leaves its user without its rule, so this was written by something else; decide before mymo touches the box",
				"if nothing depends on it: sudo deluser " + baseline.MymoUser + "; sudo rm -rf " + baseline.StateDir + " — then check again"})
		}
	}

	// --- sshd: mymo recognizes its own drop-in homes. A conflict
	// among foreign files is the operator's to resolve; anything
	// involving mymo's own work is mymo's to fix, and the setup
	// fixes it deterministically (the drop-in sorts first, the
	// retired name is removed)
	pwForeign := s.SSHDirectives["passwordauthentication"]
	switch {
	case len(s.SSHOurs["passwordauthentication"]) > 0:
		checks = append(checks, Check{"sshd", "sshd", Pass,
			"mymo's own hardening drop-in is in place — it sorts first in sshd_config.d, so it holds the effective posture", ""})
	case len(s.SSHOursOld["passwordauthentication"]) > 0:
		note := "an earlier mymo hardening drop-in sits in its retired home — the setup moves it to the front of sshd_config.d"
		if len(pwForeign) > 0 && pwForeign[0] == "yes" {
			note += " (it is shadowed right now: password auth is effectively on)"
		}
		checks = append(checks, Check{"sshd", "sshd", Pass, note, ""})
	case len(pwForeign) == 1 && pwForeign[0] == "yes":
		checks = append(checks, Check{"sshd", "sshd", Pass, "password auth is on — hardened after key access is proven", ""})
	case len(pwForeign) > 1:
		checks = append(checks, Check{"sshd", "sshd", Decide, "conflicting PasswordAuthentication values in sshd configuration — resolve or let mymo verify with sshd -T at apply",
			"sudo sshd -T | grep -i passwordauthentication shows the effective value — remove the stray override, then check again"})
	}

	return checks
}

// Verdict summarizes the audit: the worst outcome across all checks.
// Abort blocks, Decide pauses for the user, Adopt means mymo can take
// over, Pass means a clean path.
func Verdict(checks []Check) Outcome {
	worst := Pass
	for _, c := range checks {
		if c.Outcome > worst {
			worst = c.Outcome
		}
	}
	return worst
}

// Counts groups the checks by outcome for summaries.
func Counts(checks []Check) map[Outcome]int {
	m := map[Outcome]int{}
	for _, c := range checks {
		m[c.Outcome]++
	}
	return m
}

func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
