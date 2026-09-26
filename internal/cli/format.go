package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/facts"
)

// formatBytes renders a byte count in binary units, e.g. "3.9 GiB".
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	size := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, u := range units {
		size /= unit
		if size < 10 {
			return fmt.Sprintf("%.1f %s", size, u)
		}
		if size < unit {
			return fmt.Sprintf("%.0f %s", size, u)
		}
	}
	return fmt.Sprintf("%.0f TiB", size)
}

// formatUptime renders a duration the way people read it, e.g.
// "9 days 17 hours", "3 hours 12 minutes", "less than a minute".
func formatUptime(d time.Duration) string {
	d = d.Truncate(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	plural := func(n int64, unit string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s", unit)
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case days > 0:
		return plural(int64(days), "day") + " " + plural(int64(hours), "hour")
	case hours > 0:
		return plural(int64(hours), "hour") + " " + plural(int64(minutes), "minute")
	case minutes > 0:
		return plural(int64(minutes), "minute")
	default:
		return "less than a minute"
	}
}

// dockerVersion trims "Docker version 27.3.1, build ..." to "27.3.1",
// falling back to the whole line when the shape is unexpected.
func dockerVersion(line string) string {
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			return strings.TrimSuffix(fields[i+1], ",")
		}
	}
	return line
}

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
		row("uptime", formatUptime(f.Uptime))
	} else {
		row("uptime", "unknown")
	}
	if f.MemTotal > 0 {
		row("memory", formatBytes(f.MemTotal)+" total / "+formatBytes(f.MemAvail)+" available")
	} else {
		row("memory", "unknown")
	}
	if f.DiskTotal > 0 {
		row("disk (/)", formatBytes(f.DiskTotal)+" total / "+formatBytes(f.DiskFree)+" free")
	} else {
		row("disk (/)", "unknown")
	}
	if f.Docker != "" {
		row("docker", dockerVersion(f.Docker))
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
	row("user", unknown(f.User))
	row("checked", f.CollectedAt.Format(time.RFC3339))
}
