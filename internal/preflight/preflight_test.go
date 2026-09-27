package preflight

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/facts"
)

// fakeRunner answers each command from a canned table, recording what
// was asked so tests can assert the audit never mutates: every command
// must be read-only.
type fakeRunner struct {
	responses map[string]string
	codes     map[string]int
	asked     []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	key := name + " " + strings.Join(args, " ")
	r.asked = append(r.asked, key)
	if code, ok := r.codes[key]; ok {
		return r.responses[key], code, nil
	}
	out, ok := r.responses[key]
	if !ok {
		return "", 1, nil // unknown commands fail, never hang
	}
	return out, 0, nil
}

func goodNode() facts.Node {
	return facts.Node{
		Hostname: "web-1", OS: "Ubuntu 24.04.2 LTS", Arch: "amd64",
		CPUs: 2, Systemd: true, User: "amir",
		MemTotal: 4 << 30, MemAvail: 2 << 30,
		DiskTotal: 40 << 30, DiskFree: 30 << 30,
		CollectedAt: time.Now(),
	}
}

func baseRunner() *fakeRunner {
	return &fakeRunner{
		codes: map[string]int{},
		responses: map[string]string{
			"id -u":                    "1000",
			"sudo -n true":             "",
			"cat /etc/ssh/sshd_config": "PasswordAuthentication yes\nPermitRootLogin prohibit-password\n",
			"sudo -n ufw status":       "Status: inactive\n",
			"sudo -n ss -tlnp":         "LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=800,fd=3))\n",
			"sh -c command -v nginx apache2 httpd haproxy traefik 2>/dev/null":  "",
			"sh -c dpkg -l docker-ce docker.io 2>/dev/null | grep \"^ii\"":      "",
			"sh -c id mymo 2>/dev/null; test -d /var/lib/mymo && echo mymo-dir": "",
		},
	}
}

func TestScanParsesTheDeepAudit(t *testing.T) {
	r := baseRunner()
	// ufw active, caddy holding :80, nginx installed but not listening
	r.responses["sudo -n ufw status"] = "Status: active\n"
	r.responses["sudo -n ss -tlnp"] = strings.Join([]string{
		`LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=800,fd=3))`,
		`LISTEN 0 128 *:80 *:* users:(("caddy",pid=900,fd=6))`,
		`LISTEN 0 128 [::]:443 [::]:* users:(("caddy",pid=900,fd=7))`,
	}, "\n")
	r.responses["sh -c command -v nginx apache2 httpd haproxy traefik 2>/dev/null"] = "/usr/sbin/nginx\n"
	r.responses[`sh -c dpkg -l docker-ce docker.io 2>/dev/null | grep "^ii"`] = "ii  docker-ce 27.1.3-1 amd64\n"
	r.responses["sh -c docker ps -q 2>/dev/null | wc -l"] = "2\n"
	r.responses["sh -c id mymo 2>/dev/null; test -d /var/lib/mymo && echo mymo-dir"] = "uid=980(mymo) gid=980(mymo) groups=980(mymo)\n"

	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s.UID != 1000 || !s.Sudo {
		t.Errorf("privilege = uid %d sudo %v, want 1000 true", s.UID, s.Sudo)
	}
	if got := s.SSHDirectives["passwordauthentication"]; len(got) != 1 || got[0] != "yes" {
		t.Errorf("passwordauthentication = %v", got)
	}
	if s.Firewall != "active" {
		t.Errorf("firewall = %q, want active", s.Firewall)
	}
	if s.Listeners[80] != "caddy" || s.Listeners[443] != "caddy" {
		t.Errorf("listeners = %v, want caddy on 80 and 443", s.Listeners)
	}
	if _, taken := s.Listeners[22]; taken {
		t.Errorf("port 22 should not be tracked: %v", s.Listeners)
	}
	if len(s.Proxies) != 1 || s.Proxies[0] != "nginx" {
		t.Errorf("proxies = %v, want [nginx]", s.Proxies)
	}
	if s.DockerPkg != "docker-ce" || s.Containers != 2 {
		t.Errorf("docker = %q containers %d, want docker-ce 2", s.DockerPkg, s.Containers)
	}
	if !s.MymoUserUsed {
		t.Error("mymo user not detected")
	}
}

func TestScanWithoutPrivilegeSaysUnknown(t *testing.T) {
	r := baseRunner()
	r.codes["sudo -n true"] = 1
	delete(r.responses, "sudo -n true")
	r.codes["sudo -n ufw status"] = 1
	r.responses["sudo -n ufw status"] = ""
	// sudo is refused, so the privileged listing must fail too and the
	// scan falls back to the unprivileged one
	r.codes["sudo -n ss -tlnp"] = 1
	r.responses["sudo -n ss -tlnp"] = ""
	r.responses["ss -tln"] = "LISTEN 0 128 *:80 *:*\n"

	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s.Sudo {
		t.Error("sudo must be false when sudo -n fails")
	}
	if s.Firewall != "unknown" {
		t.Errorf("firewall = %q, want unknown without privilege", s.Firewall)
	}
	if _, taken := s.Listeners[80]; !taken || s.Listeners[80] != "" {
		t.Errorf("listeners = %v, want 80 taken by unknown owner", s.Listeners)
	}
	if s.DockerPkg == "" && s.Containers != 0 {
		t.Error("containers should stay 0 without docker")
	}
}

func TestScanCommandsAreReadOnly(t *testing.T) {
	r := baseRunner()
	r.responses["sudo -n ufw status"] = "Status: inactive\n"
	if _, err := Scan(context.Background(), r); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, cmd := range r.asked {
		lower := strings.ToLower(cmd)
		for _, banned := range []string{
			"rm ", "mv ", "tee ", "apt ", "apt-get", "kill", "useradd", "adduser",
			"mkdir ", "touch ", "sed -i", "chpasswd", "passwd ",
			"systemctl start", "systemctl enable", "systemctl restart", "systemctl reload",
			"ufw enable", "ufw allow", "ufw deny",
		} {
			if strings.Contains(lower, banned) {
				t.Errorf("preflight ran a mutating command: %q (matched %q)", cmd, banned)
			}
		}
		// a redirect writes a file unless it points at /dev/null
		if i := strings.Index(lower, ">"); i >= 0 && !strings.Contains(lower[i:], ">/dev/null") {
			t.Errorf("preflight ran a redirect: %q", cmd)
		}
	}
}

func TestEvaluateCleanNodePasses(t *testing.T) {
	s := Audit{
		UID: 1000, Sudo: true, Firewall: "absent",
		SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}},
		Listeners:     map[int]string{},
	}
	checks := Evaluate(goodNode(), s)
	if got := Verdict(checks); got != Pass {
		t.Errorf("clean node verdict = %v, want pass; checks %+v", got, checks)
	}
	for _, c := range checks {
		if c.Outcome != Pass {
			t.Errorf("%s/%s: %v — %s", c.Group, c.Title, c.Outcome, c.Detail)
		}
	}
}

func TestEvaluateVerdicts(t *testing.T) {
	cases := []struct {
		name  string
		f     facts.Node
		s     Audit
		want  Outcome
		title string // one check whose detail should be checked
	}{
		{
			name: "unsupported os aborts",
			f:    func() facts.Node { n := goodNode(); n.OS = "Alpine Linux v3.20"; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Abort, title: "platform",
		},
		{
			name: "unsupported arch aborts",
			f:    func() facts.Node { n := goodNode(); n.Arch = "386"; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Abort, title: "platform",
		},
		{
			name: "missing systemd aborts",
			f:    func() facts.Node { n := goodNode(); n.Systemd = false; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Abort, title: "platform",
		},
		{
			name: "no privilege aborts",
			f:    goodNode(),
			s:    Audit{UID: 1000, Firewall: "unknown", Listeners: map[int]string{}},
			want: Abort, title: "privilege",
		},
		{
			name: "thin disk aborts",
			f:    func() facts.Node { n := goodNode(); n.DiskFree = 1 << 30; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Abort, title: "disk headroom",
		},
		{
			name: "thin memory decides",
			f:    func() facts.Node { n := goodNode(); n.MemTotal = 512 << 20; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Decide, title: "memory",
		},
		{
			name: "docker.io decides",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}, DockerPkg: "docker.io"},
			want: Decide, title: "docker",
		},
		{
			name: "official docker adopts",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}, DockerPkg: "docker-ce"},
			want: Adopt, title: "docker",
		},
		{
			name: "nginx on 80 decides",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{80: "nginx"}},
			want: Decide, title: "ports",
		},
		{
			name: "unknown holder on 443 decides",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{443: ""}},
			want: Decide, title: "ports",
		},
		{
			name: "caddy on 80 and 443 adopts",
			f:    func() facts.Node { n := goodNode(); n.Caddy = "v2.8.4"; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{80: "caddy", 443: "caddy"}},
			want: Adopt, title: "ports",
		},
		{
			name: "existing mymo state decides",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}, MymoUserUsed: true, MymoDirUsed: true},
			want: Decide, title: "mymo state",
		},
	}
	for _, tc := range cases {
		checks := Evaluate(tc.f, tc.s)
		if got := Verdict(checks); got != tc.want {
			t.Errorf("%s: verdict = %v, want %v; checks %+v", tc.name, got, tc.want, checks)
		}
		var found *Check
		for i := range checks {
			if checks[i].Title == tc.title {
				found = &checks[i]
			}
		}
		if found == nil {
			t.Fatalf("%s: no check titled %q", tc.name, tc.title)
		}
		if found.Outcome != tc.want {
			t.Errorf("%s: %s outcome = %v, want %v (%s)", tc.name, tc.title, found.Outcome, tc.want, found.Detail)
		}
	}
}

func TestParseListenersOwnerWithoutRoot(t *testing.T) {
	into := map[int]string{}
	parseListeners("LISTEN 0 128 *:80 *:*\n", into, false)
	if _, taken := into[80]; !taken || into[80] != "" {
		t.Errorf("listeners = %v, want 80 taken with unknown owner", into)
	}
}

func TestCollectSSHDirectivesKeepsConflictsVisible(t *testing.T) {
	d := map[string][]string{}
	collectSSHDirectives("PasswordAuthentication yes\n", d)
	collectSSHDirectives("PasswordAuthentication no\n", d)
	if got := d["passwordauthentication"]; len(got) != 2 {
		t.Errorf("conflicting values = %v, want both kept", got)
	}
}

func TestBaselineConstantsHeldByPreflight(t *testing.T) {
	// the audit must watch exactly the ports the baseline names
	for _, p := range []int{80, 443} {
		if !portWatched(p) {
			t.Errorf("port %d not watched", p)
		}
	}
	if portWatched(22) {
		t.Error("port 22 must not be treated as a conflict")
	}
	if baseline.MymoUser != "mymo" || baseline.StateDir != "/var/lib/mymo" {
		t.Error("baseline constants drifted")
	}
}
