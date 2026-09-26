// Package facts implements mymo's discovery probe: a fixed set of
// read-only commands whose output is parsed into a snapshot of a node.
// Discovery only ever observes; it never mutates the node. The package
// depends on nothing internal — callers pass a Runner (the SSH
// transport satisfies it), which also keeps the probe testable with
// canned responses.
package facts

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// probeTimeout bounds a whole probe when the caller sets no deadline;
// each individual command is additionally bounded by the transport.
const probeTimeout = 60 * time.Second

// Runner executes one explicit-argument-vector command on a node and
// returns its combined output and exit code. A nonzero exit is a
// result, not an error; errors mean the command never completed.
// *ssh.Client satisfies Runner.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, int, error)
}

// Node is a discovered snapshot of one node. It is persisted with the
// node record; CollectedAt doubles as the last-seen marker. Zero values
// mean unknown — a missing binary or unreadable file leaves its fact
// unset rather than failing the probe.
type Node struct {
	Hostname    string        `json:"hostname,omitempty"`
	OS          string        `json:"os,omitempty"`     // /etc/os-release PRETTY_NAME
	Kernel      string        `json:"kernel,omitempty"` // uname release
	Arch        string        `json:"arch,omitempty"`
	CPUs        int           `json:"cpus,omitempty"`
	Uptime      time.Duration `json:"uptime,omitempty"`
	MemTotal    uint64        `json:"mem_total,omitempty"`     // bytes
	MemAvail    uint64        `json:"mem_available,omitempty"` // bytes
	DiskTotal   uint64        `json:"disk_total,omitempty"`    // bytes, root filesystem
	DiskFree    uint64        `json:"disk_free,omitempty"`     // bytes, root filesystem
	Docker      string        `json:"docker,omitempty"`        // version line, "" = absent
	Caddy       string        `json:"caddy,omitempty"`         // version, "" = absent
	Systemd     bool          `json:"systemd,omitempty"`
	User        string        `json:"user,omitempty"`
	CollectedAt time.Time     `json:"collected_at,omitempty"`
}

// Probe runs the discovery command set over r and returns the parsed
// snapshot. A transport failure aborts the probe with the transport's
// structured error; commands that are merely absent (nonzero exit)
// leave their facts unknown. The first command doubles as the
// reachability check.
func Probe(ctx context.Context, r Runner) (Node, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeTimeout)
		defer cancel()
	}

	var f Node

	// uname prints kernel-name, nodename, kernel-release, machine —
	// one round trip for hostname, kernel, and arch.
	if out, ok, err := one(ctx, r, "uname", "-snrm"); err != nil {
		return Node{}, err
	} else if ok {
		f.Hostname, f.Kernel, f.Arch = parseUname(out)
	}
	if out, ok, err := one(ctx, r, "cat", "/etc/os-release"); err != nil {
		return Node{}, err
	} else if ok {
		f.OS = parseOSRelease(out)
	}
	if out, ok, err := one(ctx, r, "nproc"); err != nil {
		return Node{}, err
	} else if ok {
		f.CPUs = parseInt(out)
	}
	if out, ok, err := one(ctx, r, "cat", "/proc/uptime"); err != nil {
		return Node{}, err
	} else if ok {
		f.Uptime = parseUptime(out)
	}
	if out, ok, err := one(ctx, r, "cat", "/proc/meminfo"); err != nil {
		return Node{}, err
	} else if ok {
		f.MemTotal, f.MemAvail = parseMemInfo(out)
	}
	if out, ok, err := one(ctx, r, "df", "-Pk", "/"); err != nil {
		return Node{}, err
	} else if ok {
		f.DiskTotal, f.DiskFree = parseDF(out)
	}
	if out, ok, err := one(ctx, r, "docker", "--version"); err != nil {
		return Node{}, err
	} else if ok {
		f.Docker = firstLine(out)
	}
	if out, ok, err := one(ctx, r, "caddy", "version"); err != nil {
		return Node{}, err
	} else if ok {
		f.Caddy = firstLine(out)
	}
	if _, ok, err := one(ctx, r, "systemctl", "--version"); err != nil {
		return Node{}, err
	} else if ok {
		f.Systemd = true
	}
	if out, ok, err := one(ctx, r, "whoami"); err != nil {
		return Node{}, err
	} else if ok {
		f.User = firstLine(out)
	}

	f.CollectedAt = time.Now()
	return f, nil
}

// one runs a single probe command. It returns ok=false when the command
// exited nonzero (typically 127, binary not installed); transport
// errors are returned and abort the probe.
func one(ctx context.Context, r Runner, name string, args ...string) (out string, ok bool, err error) {
	out, code, err := r.Run(ctx, name, args...)
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, nil
	}
	return out, true, nil
}

// parseUname splits "Linux nodename release machine" (the fixed field
// order uname prints for -snrm) into its facts.
func parseUname(out string) (hostname, kernel, arch string) {
	fields := strings.Fields(out)
	for i, v := range fields {
		switch i {
		case 1:
			hostname = v
		case 2:
			kernel = v
		case 3:
			arch = v
		}
	}
	return
}

// parseOSRelease extracts PRETTY_NAME from /etc/os-release output.
func parseOSRelease(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(line, "PRETTY_NAME="); found {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// parseInt reads a decimal integer, ignoring surrounding whitespace.
func parseInt(out string) int {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return n
}

// parseUptime reads /proc/uptime ("12345.67 89.01") into a duration
// rounded down to whole seconds.
func parseUptime(out string) time.Duration {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || sec < 0 {
		return 0
	}
	return (time.Duration(sec * float64(time.Second))).Truncate(time.Second)
}

// parseMemInfo reads MemTotal and MemAvailable from /proc/meminfo into
// bytes.
func parseMemInfo(out string) (total, avail uint64) {
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(line, "MemTotal:"); found {
			total = memKBytes(v)
		} else if v, found := strings.CutPrefix(line, "MemAvailable:"); found {
			avail = memKBytes(v)
		}
	}
	return
}

// memKBytes parses "  16326432 kB" into bytes.
func memKBytes(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

// parseDF reads POSIX `df -Pk /` output for the root filesystem into
// bytes. -P guarantees one line per filesystem and -k fixes blocks at
// 1024 bytes, so the first line after the header is /.
func parseDF(out string) (total, free uint64) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, 0
	}
	return blocksToBytes(fields[1]), blocksToBytes(fields[3])
}

// blocksToBytes converts a 1024-block count into bytes.
func blocksToBytes(s string) uint64 {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

// firstLine returns the first non-empty line, trimmed.
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
