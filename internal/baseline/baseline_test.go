package baseline

import "testing"

func TestMatchesOS(t *testing.T) {
	cases := []struct {
		pretty string
		want   bool
	}{
		{"Ubuntu 24.04.2 LTS", true},
		{"Ubuntu 22.04.5 LTS", true},
		{"Debian GNU/Linux 12 (bookworm)", true},
		{"Ubuntu 25.10", false},
		{"Debian GNU/Linux 13", false},
		{"Alpine Linux v3.20", false},
		{"", false},
		{"CentOS Stream 9", false},
	}
	for _, tc := range cases {
		if got := MatchesOS(tc.pretty); got != tc.want {
			t.Errorf("MatchesOS(%q) = %v, want %v", tc.pretty, got, tc.want)
		}
	}
}

func TestArchSupported(t *testing.T) {
	// kernel names as uname -m reports them
	for _, a := range []string{"x86_64", "aarch64"} {
		if !ArchSupported(a) {
			t.Errorf("kernel arch %q must be supported", a)
		}
	}
	// dpkg names as repositories use them
	for _, a := range []string{"amd64", "arm64"} {
		if !ArchSupported(a) {
			t.Errorf("package arch %q must be supported", a)
		}
	}
	for _, a := range []string{"386", "armv7l", "", "riscv64"} {
		if ArchSupported(a) {
			t.Errorf("arch %q must not be supported", a)
		}
	}
}
