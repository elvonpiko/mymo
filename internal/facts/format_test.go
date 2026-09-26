package facts

import (
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{4194304, "4.0 MiB"},
		{4106280960, "3.8 GiB"},
		{42024214528, "39 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, tc := range cases {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "less than a minute"},
		{30 * time.Second, "less than a minute"},
		{5 * time.Minute, "5 minutes"},
		{90 * time.Minute, "1 hour 30 minutes"},
		{42 * time.Hour, "1 day 18 hours"},
		{9*24*time.Hour + 17*time.Hour, "9 days 17 hours"},
	}
	for _, tc := range cases {
		if got := FormatUptime(tc.in); got != tc.want {
			t.Errorf("FormatUptime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	if got := FormatAge(time.Time{}); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{90 * time.Minute, "1h ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{20 * 24 * time.Hour, "2w ago"},
	}
	for _, tc := range cases {
		if got := FormatAge(time.Now().Add(-tc.ago)); got != tc.want {
			t.Errorf("FormatAge(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestDockerVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Docker version 27.3.1, build 29.1.3-0ubuntu3~24.04.2", "27.3.1"},
		{"Docker version 24.0.7", "24.0.7"},
		{"podman 4.3.1", "podman 4.3.1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := DockerVersion(tc.in); got != tc.want {
			t.Errorf("DockerVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
