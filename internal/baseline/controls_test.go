package baseline

import (
	"sort"
	"strings"
	"testing"
)

// The pinned values are the product decision. These tests make every
// change to them a deliberate act: to update the baseline you edit
// these expectations in the same commit as the data, with the
// baseline version bumped and the changelog updated.

func TestSSHDirectivesArePinned(t *testing.T) {
	if len(SSHDirectives) == 0 {
		t.Fatal("no ssh directives pinned")
	}
	seen := map[string]bool{}
	for _, d := range SSHDirectives {
		if seen[d.Key] {
			t.Errorf("duplicate sshd directive %q", d.Key)
		}
		seen[d.Key] = true
		if d.Source == "" {
			t.Errorf("directive %q has no provenance", d.Key)
		}
	}
	// the floor mymo promises, in so many words
	for key, want := range map[string]string{
		"PermitRootLogin":              "no",
		"PasswordAuthentication":       "no",
		"KbdInteractiveAuthentication": "no",
		"X11Forwarding":                "no",
		"AllowAgentForwarding":         "no",
		"LoginGraceTime":               "30",
	} {
		var got string
		var found bool
		for _, d := range SSHDirectives {
			if d.Key == key {
				got, found = d.Value, true
			}
		}
		if !found {
			t.Errorf("sshd directive %q missing from the pin", key)
		} else if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// the one documented deviation must stay documented
	for _, d := range SSHDirectives {
		if d.Key == "AllowTcpForwarding" {
			if !strings.Contains(d.Source, "deviation") {
				t.Errorf("AllowTcpForwarding deviation must say so: %q", d.Source)
			}
		}
	}
}

func TestSysctlsArePinned(t *testing.T) {
	// the tested floor from dev-sec's os-hardening set
	for _, key := range []string{
		"fs.protected_hardlinks", "fs.protected_symlinks", "fs.protected_fifos",
		"fs.protected_regular", "kernel.kptr_restrict", "kernel.dmesg_restrict",
		"net.ipv4.conf.all.accept_redirects", "net.ipv4.conf.all.send_redirects",
		"net.ipv4.tcp_syncookies",
	} {
		if _, ok := Sysctls[key]; !ok {
			t.Errorf("sysctl %q missing from the pin", key)
		}
	}
	for key, value := range Sysctls {
		if value == "" {
			t.Errorf("sysctl %q pinned to empty", key)
		}
	}
	// rp_filter stays unpinned on purpose
	for key := range Sysctls {
		if strings.Contains(key, "rp_filter") {
			t.Errorf("rp_filter must stay unpinned (found %q)", key)
		}
	}
}

func TestDockerInstallsOnlyTheOfficialWay(t *testing.T) {
	for _, pkg := range []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"} {
		if !contains(DockerPackages, pkg) {
			t.Errorf("package %q missing", pkg)
		}
	}
	if contains(DockerPackages, "docker.io") {
		t.Error("docker.io must never be in the baseline")
	}
	if !strings.Contains(DockerRepo, "download.docker.com") {
		t.Error("docker must come from the official apt repository")
	}
	// the daemon policy keeps logs bounded and containers alive
	if !strings.Contains(DockerDaemonConfig, `"max-size": "10m"`) ||
		!strings.Contains(DockerDaemonConfig, `"live-restore": true`) {
		t.Errorf("daemon config lost its log bounds or live-restore: %s", DockerDaemonConfig)
	}
}

func TestFirewallOnlyAdds(t *testing.T) {
	joined := strings.Join(rules(FirewallRules), "; ")
	for _, want := range []string{
		"default deny incoming", "default allow outgoing",
		"limit 22/tcp", "allow 80/tcp", "allow 443/tcp",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("firewall rule %q missing: %s", want, joined)
		}
	}
	for _, banned := range []string{"reset", "delete", "deny 80", "deny 443"} {
		if strings.Contains(joined, banned) {
			t.Errorf("firewall must never %q", banned)
		}
	}
}

func TestControlsInventory(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Controls {
		if c.ID == "" || c.Title == "" || c.Source == "" || c.Apply == "" {
			t.Errorf("control %+v is incomplete", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate control id %q", c.ID)
		}
		seen[c.ID] = true
	}
	for _, id := range []string{
		"mymo-user", "ssh-hardening", "sysctl", "unattended-upgrades",
		"firewall", "docker", "docker-daemon", "caddy", "state-dir", "baseline-marker",
	} {
		if !seen[id] {
			t.Errorf("control %q missing from the inventory", id)
		}
	}
	// the safety spine is stated in the inventory itself
	var ssh Control
	for _, c := range Controls {
		if c.ID == "ssh-hardening" {
			ssh = c
		}
	}
	if !strings.Contains(ssh.Apply, "second connection") {
		t.Error("ssh hardening must gate on a proven second connection")
	}
}

func TestNothingFetchesAtRuntime(t *testing.T) {
	// the convenience script and runtime playbook fetching are both
	// rejected by design; the pinned data must not smuggle them in
	var all strings.Builder
	for _, d := range SSHDirectives {
		all.WriteString(d.Key + d.Value + d.Source)
	}
	for k, v := range Sysctls {
		all.WriteString(k + v)
	}
	all.WriteString(strings.Join(DockerPackages, " ") + DockerRepo + DockerDaemonConfig)
	all.WriteString(CaddyRepo + CaddyPackage + strings.Join(SystemPackages, " "))
	all.WriteString(MymoSudoersDropin + MymoSudoersRule)
	for _, c := range Controls {
		all.WriteString(c.ID + c.Title + c.Source + c.Apply)
	}
	if strings.Contains(all.String(), "get.docker.com") ||
		strings.Contains(all.String(), "curl | sh") ||
		strings.Contains(all.String(), "wget ") {
		t.Error("the baseline must not reference piped install scripts")
	}
}

func TestSysctlKeysSorted(t *testing.T) {
	// deterministic order for plans and drift diffs
	keys := make([]string, 0, len(Sysctls))
	for k := range Sysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i := 1; i < len(keys); i++ {
		if keys[i-1] == keys[i] {
			t.Errorf("duplicate sysctl key %q", keys[i])
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func rules(rs []FirewallRule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Rule
	}
	return out
}
