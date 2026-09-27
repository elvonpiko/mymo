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
}

// Audit is the deep read-only scan result. Zero values mean unknown,
// never "absent" — preflight names what it could not see.
type Audit struct {
	UID  int
	Sudo bool // passwordless sudo confirmed

	// SSH directives seen in sshd_config and its includes: each maps
	// a directive to every value found. Conflicting values are
	// reported; the effective value is only proven at apply time with
	// sshd -T, which needs root.
	SSHDirectives map[string][]string

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
	// distribution ships. Nonzero exits mean unreadable or absent —
	// directives stay unknown, never guessed.
	s.SSHDirectives = map[string][]string{}
	for _, path := range []string{"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d/*.conf"} {
		out, _, err := r.Run(ctx, "cat", path)
		if err != nil {
			return Audit{}, err
		}
		collectSSHDirectives(out, s.SSHDirectives)
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
		checks = append(checks, Check{"platform", "platform", Abort, "unknown — observe the node first"})
	case f.OS == "":
		checks = append(checks, Check{"platform", "platform", Abort, "os unknown — observe the node first"})
	case !baseline.MatchesOS(f.OS):
		checks = append(checks, Check{"platform", "platform", Abort, f.OS + " — baseline " + baseline.Version + " supports " + baseline.SupportedOSShort})
	case f.Arch == "":
		checks = append(checks, Check{"platform", "platform", Abort, "architecture unknown — observe the node first"})
	case !baseline.ArchSupported(f.Arch):
		checks = append(checks, Check{"platform", "platform", Abort, f.Arch + " — no official Docker or Caddy packages"})
	case !f.Systemd:
		checks = append(checks, Check{"platform", "platform", Abort, f.OS + " · " + f.Arch + " — but no systemd; the baseline manages services through it"})
	default:
		checks = append(checks, Check{"platform", "platform", Pass, f.OS + " · " + f.Arch + " · systemd"})
	}

	// --- privilege ------------------------------------------------
	switch {
	case s.UID == 0:
		checks = append(checks, Check{"privilege", "privilege", Pass, "connected as root"})
	case s.Sudo:
		checks = append(checks, Check{"privilege", "privilege", Pass, "passwordless sudo as " + f.User})
	default:
		checks = append(checks, Check{"privilege", "privilege", Abort, "neither root nor passwordless sudo — the baseline needs privilege to install and harden"})
	}

	// --- capacity -------------------------------------------------
	switch {
	case f.DiskTotal == 0:
		checks = append(checks, Check{"disk", "disk headroom", Abort, "root filesystem size unknown — observe the node first"})
	case f.DiskFree >= baseline.MinDiskFree:
		checks = append(checks, Check{"disk", "disk headroom", Pass, facts.FormatBytes(f.DiskFree) + " free of " + facts.FormatBytes(f.DiskTotal) + " — needs " + facts.FormatBytes(baseline.MinDiskFree)})
	default:
		checks = append(checks, Check{"disk", "disk headroom", Abort, "only " + facts.FormatBytes(f.DiskFree) + " free — needs " + facts.FormatBytes(baseline.MinDiskFree) + " for the baseline's installs"})
	}

	switch {
	case f.MemTotal >= baseline.MinMemory:
		checks = append(checks, Check{"memory", "memory", Pass, facts.FormatBytes(f.MemTotal) + " total"})
	case f.MemTotal == 0:
		checks = append(checks, Check{"memory", "memory", Abort, "unknown — observe the node first"})
	default:
		checks = append(checks, Check{"memory", "memory", Decide, "only " + facts.FormatBytes(f.MemTotal) + " total — headroom for applications is thin"})
	}

	// --- what the baseline installs -------------------------------
	switch s.Firewall {
	case "active":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw is active — existing rules are kept; the baseline only adds its own"})
	case "inactive":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw is installed but inactive — the baseline enables it with its policy"})
	case "absent":
		checks = append(checks, Check{"firewall", "firewall", Pass, "ufw not installed — the baseline installs and enables it"})
	default:
		checks = append(checks, Check{"firewall", "firewall", Decide, "firewall state unknown without root — the baseline will refuse to touch a firewall it cannot see"})
	}

	switch s.DockerPkg {
	case "":
		if f.Docker != "" {
			checks = append(checks, Check{"docker", "docker", Adopt, "docker " + facts.DockerVersion(f.Docker) + " present but not from dpkg — mymo can adopt and manage it"})
		} else {
			checks = append(checks, Check{"docker", "docker", Pass, "not installed — the baseline installs docker-ce from the official repository"})
		}
	case "docker-ce":
		detail := "official docker-ce " + facts.DockerVersion(f.Docker) + " — mymo can adopt and manage it"
		if s.Containers > 0 {
			detail += ", " + strconv.Itoa(s.Containers) + " container(s) running"
		}
		checks = append(checks, Check{"docker", "docker", Adopt, detail})
	case "docker.io":
		checks = append(checks, Check{"docker", "docker", Decide, "distribution docker.io installed — only you can replace it"})
	}

	switch {
	case f.Caddy != "":
		checks = append(checks, Check{"caddy", "caddy", Adopt, "caddy " + firstToken(f.Caddy) + " present — mymo can adopt and manage it"})
	default:
		checks = append(checks, Check{"caddy", "caddy", Pass, "not installed — the baseline installs it"})
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
		checks = append(checks, Check{"ports", "ports", outcome, detail})
	}

	if len(s.Proxies) > 0 {
		checks = append(checks, Check{"proxy", "other proxies", Decide, "installed: " + strings.Join(s.Proxies, ", ") + " — mymo manages Caddy only"})
	}

	// --- existing mymo state --------------------------------------
	if s.MymoUserUsed || s.MymoDirUsed {
		what := make([]string, 0, 2)
		if s.MymoUserUsed {
			what = append(what, "user "+baseline.MymoUser+" exists")
		}
		if s.MymoDirUsed {
			what = append(what, "state dir "+baseline.StateDir+" exists")
		}
		checks = append(checks, Check{"mymo", "mymo state", Decide, strings.Join(what, "; ") + " — a previous bootstrap may be incomplete"})
	}

	// --- sshd (informational; hardening is gated at apply) ---------
	if pw := s.SSHDirectives["passwordauthentication"]; len(pw) == 1 && pw[0] == "yes" {
		checks = append(checks, Check{"sshd", "sshd", Pass, "password auth is on — hardened after key access is proven"})
	} else if len(pw) > 1 {
		checks = append(checks, Check{"sshd", "sshd", Decide, "conflicting PasswordAuthentication values in sshd configuration — resolve or let mymo verify with sshd -T at apply"})
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
