package cli

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/facts"
)

// printFacts renders a discovered snapshot the way node check
// reports it: aligned "key: value" rows, unknown facts labeled
// honestly, absent tools named as absent.
func printFacts(w io.Writer, f facts.Node) {
	row := func(key, value string) {
		fmt.Fprintf(w, "%-11s %s\n", key+":", value)
	}
	unknown := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}
	row("host", unknown(f.Hostname))
	row("os", unknown(f.OS))
	row("kernel", unknown(f.Kernel))
	row("arch", unknown(f.Arch))
	if f.CPUs > 0 {
		row("cpus", strconv.Itoa(f.CPUs))
	} else {
		row("cpus", "unknown")
	}
	if f.Uptime > 0 {
		row("uptime", facts.FormatUptime(f.Uptime))
	} else {
		row("uptime", "unknown")
	}
	if f.MemTotal > 0 {
		row("memory", facts.FormatBytes(f.MemTotal)+" total / "+facts.FormatBytes(f.MemAvail)+" available")
	} else {
		row("memory", "unknown")
	}
	if f.DiskTotal > 0 {
		row("disk (/)", facts.FormatBytes(f.DiskTotal)+" total / "+facts.FormatBytes(f.DiskFree)+" free")
	} else {
		row("disk (/)", "unknown")
	}
	if f.Docker != "" {
		row("docker", facts.DockerVersion(f.Docker))
	} else {
		row("docker", "not installed")
	}
	if f.Caddy != "" {
		row("caddy", f.Caddy)
	} else {
		row("caddy", "not installed")
	}
	if f.Systemd {
		row("systemd", "yes")
	} else {
		row("systemd", "no")
	}
	if rec, ok := facts.ParseBaselineMarker(f.BaselineMarker); ok {
		if rec.Baseline != baseline.Version {
			row("baseline", rec.Baseline+" recorded — mymo pins "+baseline.Version)
		} else {
			row("baseline", rec.Baseline+" applied "+rec.AppliedAt)
		}
	}
	row("user", unknown(f.User))
	row("checked", f.CollectedAt.Format(time.RFC3339))
}
