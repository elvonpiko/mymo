package preflight

import (
	"context"
	"fmt"
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

// sshForeignKey is the scan's one command for the main sshd_config
// plus every drop-in that is not mymo's own home.
var sshForeignKey = "sh -c for f in /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf; do " +
	"case \"$f\" in " + baseline.SSHDropin + "|" + baseline.SSHRetiredDropin + ") continue;; esac; " +
	"cat \"$f\" 2>/dev/null || true; done"

func baseRunner() *fakeRunner {
	return &fakeRunner{
		codes: map[string]int{},
		responses: map[string]string{
			"id -u":              "1000",
			"sudo -n true":       "",
			sshForeignKey:        "PasswordAuthentication yes\nPermitRootLogin prohibit-password\n",
			"sudo -n ufw status": "Status: inactive\n",
			"sudo -n ss -tlnp":   "LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=800,fd=3))\n",
			"sh -c command -v nginx apache2 httpd haproxy traefik 2>/dev/null":  "",
			"sh -c dpkg -l docker-ce docker.io 2>/dev/null | grep \"^ii\"":      "",
			"sh -c id mymo 2>/dev/null; test -d /var/lib/mymo && echo mymo-dir": "",
		},
	}
}

// findRow returns the sshd check, whatever its outcome.
func findRow(t *testing.T, checks []Check) (Check, bool) {
	t.Helper()
	for _, c := range checks {
		if c.Group == "sshd" || c.Title == "sshd" {
			return c, true
		}
	}
	return Check{}, false
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
		{
			name: "foreign docker daemon config decides",
			f:    goodNode(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}, DockerPkg: "docker-ce", DockerDaemonCfg: `{"log-driver":"syslog"}`},
			want: Decide, title: "docker",
		},
		{
			name: "existing caddyfile decides",
			f:    func() facts.Node { n := goodNode(); n.Caddy = "v2.8.4"; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}, CaddyConfig: "example.com {\n\treverse_proxy localhost:8080\n}"},
			want: Decide, title: "caddy",
		},
		{
			name: "clean caddy adopt still adopts",
			f:    func() facts.Node { n := goodNode(); n.Caddy = "v2.8.4"; return n }(),
			s:    Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{}},
			want: Adopt, title: "caddy",
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

func TestScanReadsTheFingerprintAndMarker(t *testing.T) {
	r := baseRunner()
	r.responses["sh -c id mymo 2>/dev/null; test -d /var/lib/mymo && echo mymo-dir"] =
		"uid=980(mymo) gid=980(mymo) groups=980(mymo)\nmymo-dir\n"
	r.responses["sudo -n cat "+baseline.MymoSudoersDropin] = baseline.MymoSudoersRule + "\n"
	r.responses["sudo -n cat /var/lib/mymo/baseline.json"] =
		"{\"baseline\": \"0.1\", \"appliedAt\": \"2026-02-15T10:00:00Z\"}\n"
	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s.MymoSudoers != baseline.MymoSudoersRule {
		t.Errorf("sudoers fingerprint = %q", s.MymoSudoers)
	}
	if rec, ok := facts.ParseBaselineMarker(s.MymoMarker); !ok || rec.Baseline != "0.1" {
		t.Errorf("marker = %q (parsed ok=%v)", s.MymoMarker, ok)
	}
}

func TestEvaluateMymoStateFingerprint(t *testing.T) {
	marker := func(version string) string {
		return fmt.Sprintf("{\"baseline\": %q, \"appliedAt\": \"2026-02-15T10:00:00Z\"}", version)
	}
	cases := []struct {
		name   string
		s      Audit
		want   Outcome
		detail string
	}{
		{
			name: "mymo's own recorded state adopts",
			s: Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{},
				MymoUserUsed: true, MymoDirUsed: true,
				MymoSudoers: baseline.MymoSudoersRule, MymoMarker: marker(baseline.Version)},
			want: Adopt, detail: "steps keep what exists",
		},
		{
			name: "an interrupted bootstrap adopts and resumes",
			s: Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{},
				MymoUserUsed: true, MymoDirUsed: true,
				MymoSudoers: baseline.MymoSudoersRule},
			want: Adopt, detail: "apply resumes where it stopped",
		},
		{
			name: "a different recorded baseline decides",
			s: Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{},
				MymoUserUsed: true, MymoDirUsed: true,
				MymoSudoers: baseline.MymoSudoersRule, MymoMarker: marker("0.0")},
			want: Decide, detail: "upgrades are explicit",
		},
		{
			name: "a marker without mymo's rule decides",
			s: Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{},
				MymoDirUsed: true, MymoMarker: marker(baseline.Version)},
			want: Decide, detail: "refuses to guess who did what",
		},
	}
	for _, tc := range cases {
		checks := Evaluate(goodNode(), tc.s)
		var found *Check
		for i := range checks {
			if checks[i].Title == "mymo state" {
				found = &checks[i]
			}
		}
		if found == nil {
			t.Errorf("%s: no mymo state check", tc.name)
			continue
		}
		if found.Outcome != tc.want {
			t.Errorf("%s: outcome = %s, want %s (%s)", tc.name, found.Outcome, tc.want, found.Detail)
		}
		if !strings.Contains(found.Detail, tc.detail) {
			t.Errorf("%s: detail = %q, want it to say %q", tc.name, found.Detail, tc.detail)
		}
	}
}

// TestScanWithRefusedPrivilegeNeverFingersTheBox proves the live
// regression stays dead: on the first real box, a non-root operator's
// denied read of the root-only sudoers file folded "Permission
// denied" into the fingerprint and preflight reported a rule mymo
// itself had just written as missing. A refused read is an unknown —
// and the privilege abort, not a fingerprint verdict, is what the
// operator sees.
func TestScanWithRefusedPrivilegeNeverFingersTheBox(t *testing.T) {
	r := baseRunner()
	r.responses["sh -c id mymo 2>/dev/null; test -d /var/lib/mymo && echo mymo-dir"] =
		"uid=980(mymo) gid=980(mymo) groups=980(mymo)\nmymo-dir\n"
	// the box refuses the operator's privilege entirely — the
	// shape the live bug hid behind: root-only files, no way to
	// read them, and a user that is genuinely there
	r.codes["sudo -n true"] = 1
	r.responses["sudo -n cat "+baseline.MymoSudoersDropin] = ""
	r.codes["sudo -n cat "+baseline.MymoSudoersDropin] = 1
	r.responses["sudo -n cat /var/lib/mymo/baseline.json"] = ""
	r.codes["sudo -n cat /var/lib/mymo/baseline.json"] = 1

	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s.Sudo {
		t.Fatal("the box that refuses sudo must not be recorded as having it")
	}
	if s.MymoSudoers != "" || s.MymoMarker != "" {
		t.Fatalf("a refused read fabricated a fingerprint: %q / %q", s.MymoSudoers, s.MymoMarker)
	}
	if !s.MymoUserUsed {
		t.Fatal("the box genuinely carries the mymo user")
	}
	// the operator's way in is blocked by the privilege abort
	// before any mymo-state verdict can mislead
	checks := Evaluate(facts.Node{User: "ubuntu"}, s)
	var sawAbort bool
	for _, c := range checks {
		if c.Outcome == Abort && c.Title == "privilege" {
			sawAbort = true
		}
	}
	if !sawAbort {
		t.Fatal("a refused privilege must abort before any mymo-state verdict")
	}
}

// mymo recognizes its own work by its exact bytes: a daemon policy
// that matches mymo's own is not a foreign configuration, and the
// box is never asked to review what mymo itself wrote. This is the
// bug a resumed setup met in the wild — mymo installed docker,
// failed later, and then refused to recognize its own daemon.json.
func TestMymoRecognizesItsOwnDaemonPolicy(t *testing.T) {
	f := goodNode()
	f.Docker = "27.3.1"
	s := Audit{UID: 0, Firewall: "absent", Listeners: map[int]string{},
		DockerPkg: "docker-ce", DockerDaemonOurs: true}
	checks := Evaluate(f, s)
	for _, c := range checks {
		if c.Group != "docker" {
			continue
		}
		if c.Outcome != Adopt {
			t.Fatalf("mymo's own daemon policy is not adoptable: %+v", c)
		}
		if !strings.Contains(c.Detail, "mymo's own daemon policy") {
			t.Errorf("the adoption does not say whose policy it is: %q", c.Detail)
		}
	}
}

// The scan itself decides ours from foreign: only a daemon.json that
// differs from mymo's policy is reported as needing review, and only
// a Caddyfile without mymo's import line is.
func TestScanRecognizesItsOwnConfigs(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{}}
	r.responses[`sh -c test -f /etc/docker/daemon.json && cat /etc/docker/daemon.json`] = baseline.DockerDaemonConfig + "\n"
	r.responses[`sh -c test -f /etc/caddy/Caddyfile && cat /etc/caddy/Caddyfile`] = "example.com {\n\timport mymo/*.caddy\n}\n"
	a, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !a.DockerDaemonOurs || a.DockerDaemonCfg != "" {
		t.Errorf("mymo's own policy read as foreign: ours=%v cfg=%q", a.DockerDaemonOurs, a.DockerDaemonCfg)
	}
	if !a.CaddyOurs || a.CaddyConfig != "" {
		t.Errorf("mymo's own Caddyfile read as foreign: ours=%v cfg=%q", a.CaddyOurs, a.CaddyConfig)
	}

	r.responses[`sh -c test -f /etc/docker/daemon.json && cat /etc/docker/daemon.json`] = `{"log-driver":"syslog"}`
	r.responses[`sh -c test -f /etc/caddy/Caddyfile && cat /etc/caddy/Caddyfile`] = "example.com {\n\treverse_proxy x:1\n}\n"
	a, err = Scan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if a.DockerDaemonOurs || a.DockerDaemonCfg == "" {
		t.Errorf("a foreign policy read as ours: %+v", a)
	}
	if a.CaddyOurs || a.CaddyConfig == "" {
		t.Errorf("a foreign Caddyfile read as ours: %+v", a)
	}
}

// Every finding that stands between a box and the plan names its
// way out: no "resolve it and check again" without saying what to
// do on the box. Decisions and refusals carry fixes; passes and
// adoptions do not need them.
func TestEveryStandingFindingNamesItsFix(t *testing.T) {
	f := goodNode()
	cases := []struct {
		name string
		s    Audit
		f    facts.Node
	}{
		{"foreign daemon policy", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{}, DockerPkg: "docker-ce",
			DockerDaemonCfg: `{"log-driver":"syslog"}`, SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"distribution docker", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{},
			DockerPkg: "docker.io", SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"foreign caddyfile", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{},
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, func() facts.Node { n := f; n.Caddy = "v2.8.4"; return n }()},
		{"held ports and proxies", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{80: "nginx"},
			Proxies: []string{"nginx"}, SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"no privilege", Audit{UID: 1000, Sudo: false, Firewall: "absent", Listeners: map[int]string{},
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"thin memory", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{},
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, func() facts.Node { n := f; n.MemTotal = 512 << 20; return n }()},
		{"unseen firewall", Audit{UID: 1000, Sudo: true, Firewall: "unknown", Listeners: map[int]string{},
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"intruder user", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{}, MymoUserUsed: true,
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes"}}}, f},
		{"conflicting sshd", Audit{UID: 0, Sudo: true, Firewall: "absent", Listeners: map[int]string{},
			SSHDirectives: map[string][]string{"passwordauthentication": {"yes", "no"}}}, f},
	}
	for _, tc := range cases {
		for _, c := range Evaluate(tc.f, tc.s) {
			switch c.Outcome {
			case Decide, Abort:
				if c.Fix == "" {
					t.Errorf("%s: %q (%s) stands without its fix", tc.name, c.Title, c.Detail)
				}
			}
		}
	}
}

// mymo recognizes its own sshd drop-in homes: values there are its
// own work, never a foreign conflict the operator has to resolve.
// A box carrying the retired home — the shadowed earlier attempt —
// reads as Pass with the truth about the shadow, and the setup
// re-homes it to the front of sshd_config.d.
func TestScanRecognizesItsOwnSSHDropinHomes(t *testing.T) {
	pinned := "PasswordAuthentication no\n"
	r := baseRunner()
	r.responses["cat "+baseline.SSHRetiredDropin] = pinned
	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.SSHOursOld["passwordauthentication"]; len(got) != 1 || got[0] != "no" {
		t.Fatalf("retired home values = %v", got)
	}
	// foreign files still say yes: the shadow is the truth the row must name
	checks := Evaluate(goodNode(), s)
	row, rowOK := findRow(t, checks)
	if !rowOK {
		t.Fatal("no sshd row")
	}
	if row.Outcome != Pass {
		t.Errorf("a mymo-owned drop-in conflict decided against the operator: %s", row.Outcome)
	}
	if !strings.Contains(row.Detail, "retired home") || !strings.Contains(row.Detail, "shadowed") {
		t.Errorf("the row must say whose work it is and what state it is in: %s", row.Detail)
	}

	// the current home holds the posture by ordering, even against a
	// foreign "yes"
	r2 := baseRunner()
	r2.responses["cat "+baseline.SSHDropin] = pinned
	s2, err := Scan(context.Background(), r2)
	if err != nil {
		t.Fatal(err)
	}
	row2, ok2 := findRow(t, Evaluate(goodNode(), s2))
	if !ok2 {
		t.Fatal("no sshd row")
	}
	if row2.Outcome != Pass || !strings.Contains(row2.Detail, "sorts first") {
		t.Errorf("the current home must read as holding the posture: %s / %s", row2.Outcome, row2.Detail)
	}
}

// Two foreign files claiming the same directive is still the
// operator's to resolve — the decision stays.
func TestForeignSSHConflictStillDecides(t *testing.T) {
	r := baseRunner()
	r.responses[sshForeignKey] = "PasswordAuthentication yes\nPasswordAuthentication no\n"
	s, err := Scan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findRow(t, Evaluate(goodNode(), s))
	if !ok {
		t.Fatal("no sshd row")
	}
	if row.Outcome != Decide || row.Fix == "" {
		t.Errorf("a foreign conflict must stay a decision with its fix: %s / %q", row.Outcome, row.Fix)
	}
}
