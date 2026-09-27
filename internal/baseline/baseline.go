// Package baseline pins mymo's App Host baseline: the exact, versioned
// set of controls mymo applies when a node is prepared as an app host,
// and the assumptions preflight audits against. Nothing here mutates a
// node — this package is the specification the plan is generated from.
//
// Baseline 0.1 selects a deliberately small set of controls:
//
//   - ssh: a dedicated mymo-managed admin user with mymo's key, and
//     sshd hardened (no password auth, no root login) only after key
//     access is proven over a second connection
//   - firewall: ufw, default-deny incoming / allow outgoing, SSH,
//     HTTP, and HTTPS permitted
//   - docker: Docker Engine from Docker's official apt repository
//     (never the distribution docker.io package, never the convenience
//     script), with buildx and the compose plugin, log rotation, and
//     live-restore
//   - caddy: Caddy from the official Cloudsmith apt repository, the
//     only reverse proxy mymo supports
//   - system: mymo's remote state under /var/lib/mymo; the connecting
//     user is never assumed to be root
//
// When upstream guidance changes, the baseline is reviewed and
// re-released as a new version; existing nodes record which version
// they run and are never silently upgraded.
package baseline

// Version names the pinned baseline. It is stored on prepared nodes
// and shown in preflight output.
const Version = "0.1"

// MinDiskFree is the root-filesystem headroom a node needs before
// the baseline's installs are attempted. Docker Engine plus Caddy
// and their dependencies land well under this; preflight fails rather
// than installs into a full disk.
const MinDiskFree = 2 << 30 // 2 GiB

// MinMemory is the comfortable floor for running single-container
// applications under Docker. Smaller nodes are not rejected — the
// verdict asks for a decision.
const MinMemory = 1 << 30 // 1 GiB

// MymoUser is the dedicated admin user the baseline creates and mymo
// connects through afterward.
const MymoUser = "mymo"

// StateDir is mymo's remote state directory on an app host.
const StateDir = "/var/lib/mymo"

// SupportedOS lists the distribution versions baseline 0.1 is tested
// against, matched against the PRETTY_NAME of /etc/os-release.
var SupportedOS = []string{
	"Ubuntu 22.04",
	"Ubuntu 24.04",
	"Debian GNU/Linux 12",
}

// SupportedOSShort is the display form of the supported list for
// verdict details: short, the same truth, no version-suffix drift.
const SupportedOSShort = "Ubuntu 22.04/24.04 · Debian 12"

// SupportedArch lists the package-repository names of the
// architectures with official Docker and Caddy packages; kernels
// report their own names and are mapped by ArchSupported.
var SupportedArch = []string{
	"amd64",
	"arm64",
}

// HTTPPorts are the public ports Caddy owns. Preflight audits who
// listens on them; no other service may keep them.
var HTTPPorts = []int{80, 443}

// ConflictingProxies are proxy or web-server binaries that conflict
// with mymo's Caddy-only model. mymo never removes or reconfigures
// them — their presence is a decision, made by the user, not by mymo.
var ConflictingProxies = []string{
	"nginx",
	"apache2",
	"httpd",
	"haproxy",
	"traefik",
}

// MatchesOS reports whether an os-release PRETTY_NAME names a
// supported distribution version. A point release like "Ubuntu 24.04.2
// LTS" matches the "Ubuntu 24.04" prefix.
func MatchesOS(pretty string) bool {
	for _, os := range SupportedOS {
		if len(pretty) >= len(os) && pretty[:len(os)] == os {
			return true
		}
	}
	return false
}

// ArchSupported reports whether a kernel architecture — as uname -m
// reports it — has official Docker and Caddy packages. Kernel names
// (x86_64, aarch64) are mapped onto the dpkg names the package
// repositories use.
func ArchSupported(unameM string) bool {
	switch unameM {
	case "amd64", "x86_64", "arm64", "aarch64":
		return true
	}
	return false
}
