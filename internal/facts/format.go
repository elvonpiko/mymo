package facts

import (
	"fmt"
	"strings"
	"time"
)

// FormatBytes renders a byte count in binary units, e.g. "3.8 GiB".
func FormatBytes(n uint64) string {
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

// FormatUptime renders a duration the way people read it, e.g.
// "9 days 16 hours", "3 hours 12 minutes", "less than a minute".
func FormatUptime(d time.Duration) string {
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

// FormatUptimeShort renders the same uptime in glance form: "9d 21h",
// "18h 32m", "32m". Records keep the long form; cards take this one.
func FormatUptimeShort(d time.Duration) string {
	d = d.Truncate(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// FormatAge renders how long ago t happened: "just now", "5m ago",
// "3h ago", "2d ago", "6w ago".
func FormatAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}

// DockerVersion extracts the bare version from a "Docker version X,
// build Y" line, falling back to the whole line when the shape is
// unexpected.
func DockerVersion(line string) string {
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			return strings.TrimSuffix(fields[i+1], ",")
		}
	}
	return line
}
