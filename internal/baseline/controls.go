// Control data for mymo baseline 0.1.
//
// Every value here is a deliberate, pinned decision with its
// provenance in the Source field. The plan generator walks these
// structures; verify re-checks them on the node. Changing any value
// is a baseline revision: bump Version, update the changelog in
// docs/baseline, and never push revisions onto nodes silently — nodes
// record the baseline they run and re-prepare only when told to.
//
// The selection follows dev-sec's hardening collection (the
// community-tested pragmatic subset of CIS-style guidance) and the
// upstreams' own official install paths. Where mymo deviates from
// dev-sec, the deviation is explicit here with its reason.

package baseline

import "fmt"

// SSHDropin is the sshd configuration mymo owns on an app host. mymo
// never rewrites /etc/ssh/sshd_config — it writes a drop-in, which
// Ubuntu 22.04+/Debian 12 load first through their Include line, and
// removes it wholesale on rollback.
const SSHDropin = "/etc/ssh/sshd_config.d/60-mymo.conf"

// SSHDirective is one pinned sshd setting.
type SSHDirective struct {
	Key    string
	Value  string
	Source string
}

// SSHDirectives is the hardening drop-in's global content, following
// dev-sec's ssh-hardening role with no exceptions.
//
// The sequence matters: these are written only AFTER key access is
// proven through a second connection, and sshd -T must confirm the
// effective values before sshd reloads.
var SSHDirectives = []SSHDirective{
	{"PermitRootLogin", "no", "dev-sec ssh-hardening"},
	{"PasswordAuthentication", "no", "dev-sec ssh-hardening"},
	{"KbdInteractiveAuthentication", "no", "dev-sec ssh-hardening"},
	{"PubkeyAuthentication", "yes", "explicit pin: key access is mymo's only path"},
	{"X11Forwarding", "no", "dev-sec ssh-hardening"},
	{"AllowAgentForwarding", "no", "dev-sec ssh-hardening"},
	{"AllowTcpForwarding", "no", "dev-sec ssh-hardening; tunneling is restored per-account by the carve-out below"},
	{"UseDNS", "no", "dev-sec ssh-hardening"},
	{"LoginGraceTime", "30", "dev-sec ssh-hardening (30s)"},
	{"MaxAuthTries", "3", "hardening guidance (CIS: 4 or fewer)"},
}

// SSHMatchAll closes any Match block so a drop-in's scope can never
// leak into the parent sshd configuration.
const SSHMatchAll = "Match all"

// SSHForwardingCarveOut restores TCP forwarding for the accounts
// that need it, the way sshd itself does per-user policy: a Match
// block. The global posture stays dev-sec's no-forwarding; the
// carve-out names
//
//   - the mymo account, always: mymo never publishes app ports, an
//     ssh -L tunnel is the supported access path to a container, and
//     the operator holds mymo's key — the tunnel path must exist on
//     every prepared node
//   - the operator's own account, when it is a regular login: their
//     interactive workflow is never degraded by mymo
//
// Root is never carved out (root login is disabled anyway) and the
// block is always closed with Match all.
func SSHForwardingCarveOut(operator string) ([]string, error) {
	users := MymoUser
	if operator != "" && operator != "root" && operator != MymoUser {
		if !validSSHUser(operator) {
			return nil, fmt.Errorf("operator account %q is not a valid unix name", operator)
		}
		users = operator + "," + MymoUser
	}
	return []string{
		"Match User " + users,
		"    AllowTcpForwarding yes",
		SSHMatchAll,
	}, nil
}

// KernelToPackageArch maps a kernel architecture (uname -m) onto the
// dpkg name the package repositories use; "" when unsupported.
func KernelToPackageArch(unameM string) string {
	switch unameM {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}

// validSSHUser accepts the conservative login-name shape sshd
// configurations are written against: lowercase letters, digits,
// underscore and hyphen, starting with a letter or underscore, at
// most 32 characters. It exists so an untrusted name can never be
// interpolated into an sshd file.
func validSSHUser(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_' || r == '-':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Sysctls are the kernel and network parameters the baseline pins.
// Nearly all are already Ubuntu/Debian defaults — pinning them means
// a node cannot drift below the floor on a setting the community has
// tested. rp_filter is pinned to loose mode (2): that is what the
// distributions and systemd ship on modern kernels, while strict mode
// (1) breaks legitimate asymmetric routing and is discouraged for
// general hosts by the kernel's own documentation. One value, one
// story, across the fleet.
//
// Subset of dev-sec's os-hardening sysctl set.
var Sysctls = map[string]string{
	"fs.protected_hardlinks":                    "1",
	"fs.protected_symlinks":                     "1",
	"fs.protected_fifos":                        "1",
	"fs.protected_regular":                      "2",
	"fs.suid_dumpable":                          "0",
	"kernel.kptr_restrict":                      "2",
	"kernel.dmesg_restrict":                     "1",
	"kernel.sysrq":                              "0",
	"kernel.kexec_load_disabled":                "1",
	"net.ipv4.conf.all.accept_redirects":        "0",
	"net.ipv4.conf.default.accept_redirects":    "0",
	"net.ipv4.conf.all.send_redirects":          "0",
	"net.ipv4.conf.default.send_redirects":      "0",
	"net.ipv4.conf.all.accept_source_route":     "0",
	"net.ipv4.conf.default.accept_source_route": "0",
	"net.ipv6.conf.all.accept_redirects":        "0",
	"net.ipv6.conf.default.accept_redirects":    "0",
	"net.ipv4.tcp_syncookies":                   "1",
	"net.ipv4.icmp_echo_ignore_broadcasts":      "1",
	"net.ipv4.conf.all.rp_filter":               "2",
	"net.ipv4.conf.default.rp_filter":           "2",
}

// FirewallRule is one ufw policy line, in application order.
type FirewallRule struct {
	Rule   string
	Source string
}

// FirewallRules is the firewall policy: default-deny incoming,
// SSH rate-limited, the web ports open for Caddy. Existing rules are
// never reset or removed — the plan only adds.
var FirewallRules = []FirewallRule{
	{"default deny incoming", "standard ufw policy"},
	{"default allow outgoing", "standard ufw policy"},
	{"limit 22/tcp", "ufw rate-limiting: 6 connections per 30s per source"},
	{"allow 80/tcp", "caddy: the public edge"},
	{"allow 443/tcp", "caddy: the public edge"},
}

// DockerPackages is what the baseline installs, in order, from the
// official Docker apt repository. The distribution's docker.io and
// the convenience script are both rejected by design — preflight
// refuses docker.io and the plan never fetches get.docker.com.
var DockerPackages = []string{
	"docker-ce",
	"docker-ce-cli",
	"containerd.io",
	"docker-buildx-plugin",
	"docker-compose-plugin",
}

// DockerRepo identifies the one install path mymo accepts.
const DockerRepo = "official Docker apt repository (download.docker.com/linux), stable channel"

// DockerDaemonConfig is the /etc/docker/daemon.json content: bounded
// container logs so no app can fill the disk, live-restore so
// containers survive a daemon upgrade, nothing else — mymo keeps
// Docker's own defaults everywhere it has no opinion.
const DockerDaemonConfig = `{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" },
  "live-restore": true
}`

// CaddyRepo and CaddyPackage: Caddy comes from its official Cloudsmith
// repository — the only reverse proxy mymo supports, installed the
// way the Caddy project documents, never from a distribution fork.
const (
	CaddyRepo    = "official Caddy Cloudsmith apt repository (dl.cloudsmith.io/public/caddy/stable), stable channel"
	CaddyPackage = "caddy"
)

// SystemPackages ride along with the baseline: the firewall itself
// and automatic security updates. unattended-upgrades is configured
// with the distribution defaults — security updates apply
// automatically; reboots stay manual and are always announced in the
// plan, never performed.
var SystemPackages = []string{
	"ufw",
	"unattended-upgrades",
}

// MymoSudoersDropin grants the mymo admin user passwordless sudo —
// the one privilege mymo's apply and verify steps need to run
// unattended. The mitigations are structural: the user is created by
// and for mymo, can only be reached with mymo's generated key, and
// the user's original account is left completely untouched as the
// escape hatch. mymo never edits the main sudoers file.
const (
	MymoSudoersDropin = "/etc/sudoers.d/60-mymo"
	MymoSudoersRule   = "mymo ALL=(ALL) NOPASSWD: ALL"
)

// Control is the flat, human-readable inventory of the baseline: the
// plan states these one by one, and verify walks them one by one.
type Control struct {
	ID     string
	Title  string
	Source string
	Apply  string
}

// Controls is baseline 0.1's complete inventory. Adding a control is
// a baseline revision; removing one is a baseline revision; nothing
// here is negotiable at apply time.
var Controls = []Control{
	{
		ID:     "mymo-user",
		Title:  "mymo admin user",
		Source: "mymo design",
		Apply:  "create the dedicated mymo user with mymo's generated key; your original account is left untouched as the escape hatch",
	},
	{
		ID:     "ssh-hardening",
		Title:  "sshd hardening",
		Source: "dev-sec ssh-hardening",
		Apply:  "write the pinned sshd drop-in (no root, no passwords, no X11, rate-limited grace) and reload sshd only after a second connection proves key access",
	},
	{
		ID:     "sysctl",
		Title:  "kernel hardening",
		Source: "dev-sec os-hardening (subset)",
		Apply:  "pin the tested sysctl floor: protected links and fifos, restricted kernel pointers and dmesg, no redirects or source routes, syncookies on",
	},
	{
		ID:     "unattended-upgrades",
		Title:  "security updates",
		Source: "distribution practice",
		Apply:  "install unattended-upgrades so security patches apply automatically; reboots stay manual and announced",
	},
	{
		ID:     "firewall",
		Title:  "firewall",
		Source: "standard ufw policy",
		Apply:  "install ufw, default-deny incoming, rate-limit SSH, open 80 and 443; existing rules are kept, never reset",
	},
	{
		ID:     "docker",
		Title:  "docker engine",
		Source: "Docker's official install docs",
		Apply:  "install docker-ce, containerd, the CLI, buildx and the compose plugin from the official apt repository; never docker.io, never the convenience script",
	},
	{
		ID:     "docker-daemon",
		Title:  "docker daemon policy",
		Source: "mymo design",
		Apply:  "bound container logs to 10m x 3 files and enable live-restore so containers survive daemon upgrades",
	},
	{
		ID:     "caddy",
		Title:  "caddy",
		Source: "Caddy's official install docs",
		Apply:  "install caddy from its official Cloudsmith repository — the only reverse proxy mymo manages; validate before activate, reload gracefully",
	},
	{
		ID:     "state-dir",
		Title:  "mymo state directory",
		Source: "mymo design",
		Apply:  "own /var/lib/mymo (0750 mymo:mymo) for the baseline marker, compose files, and app state",
	},
	{
		ID:     "baseline-marker",
		Title:  "baseline marker",
		Source: "mymo design",
		Apply:  "record the applied baseline version in /var/lib/mymo/baseline.json so drift is detectable and upgrades are explicit",
	},
}
