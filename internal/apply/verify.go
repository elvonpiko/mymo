package apply

import (
	"context"
	"fmt"
	"strings"

	"github.com/elvonpiko/mymo/internal/baseline"
	"github.com/elvonpiko/mymo/internal/preflight"
)

// Verify proves the baseline is live after apply — effective state,
// not config files: the sshd posture sshd itself enforces, the
// firewall's real status, the docker daemon answering, the mymo user,
// the baseline marker with the right version, and the mymo key
// working on a fresh second connection (the spec's "verify again").
//
// A check that cannot run counts as failed: verify never reports what
// it did not see.
func Verify(ctx context.Context, r preflight.Runner, o Options, version string) ([]preflight.Check, bool) {
	var checks []preflight.Check
	ok := true
	add := func(title string, passed bool, detail string) {
		outcome := preflight.Pass
		if !passed {
			outcome = preflight.Abort
		}
		checks = append(checks, preflight.Check{Group: "verify", Title: title, Outcome: outcome, Detail: detail})
		if !passed {
			ok = false
		}
	}

	// sshd's effective posture — the one view root can see and the
	// one that matters after a reload
	out, code, _ := runArgv(ctx, r, o.wrap("sshd", "-T"))
	if code != 0 {
		add("sshd posture", false, "sshd -T did not answer — the live posture is unknown")
	} else {
		posture := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) == 2 {
				posture[f[0]] = f[1]
			}
		}
		for _, d := range baseline.SSHDirectives {
			key := strings.ToLower(d.Key)
			got := posture[key]
			if got != d.Value {
				add("sshd posture", false, fmt.Sprintf("effective %s is %q — the baseline expects %q", d.Key, got, d.Value))
			}
		}
		if posture["permitrootlogin"] != "" && posture["passwordauthentication"] != "" {
			add("sshd posture", true, "no root, no passwords — the live posture")
		}
	}

	// the firewall's real status
	out, code, _ = runArgv(ctx, r, o.wrap("ufw", "status"))
	add("firewall", code == 0 && strings.HasPrefix(strings.TrimSpace(out), "Status: active"),
		"ufw is active — the plan's rules are enforcing")

	// the docker daemon answering is the only honest proof docker works
	_, code, _ = runArgv(ctx, r, o.wrap("docker", "info"))
	add("docker", code == 0, "the docker daemon answers")

	// the mymo user exists — created by the plan, required by the key
	out, code, _ = runArgv(ctx, r, o.wrap("id", baseline.MymoUser))
	add("mymo user", code == 0, "the mymo admin user exists")

	// the baseline marker: versioned, applied at a real time
	out, code, _ = runArgv(ctx, r, o.wrap("cat", baseline.StateDir+"/baseline.json"))
	switch {
	case code != 0:
		add("baseline marker", false, "no marker in "+baseline.StateDir+" — drift cannot be detected")
	case !strings.Contains(out, fmt.Sprintf("%q: %q", "baseline", version)):
		add("baseline marker", false, "the marker names a different baseline version")
	case strings.Contains(out, appliedAtPlaceholder):
		add("baseline marker", false, "the marker was never stamped with the apply time")
	default:
		add("baseline marker", true, "version recorded for drift detection")
	}

	// caddy and unattended-upgrades: installed and enabled by the plan
	out, code, _ = runArgv(ctx, r, o.wrap("systemctl", "is-enabled", "caddy"))
	add("caddy", code == 0 && strings.TrimSpace(out) == "enabled", "installed and enabled")
	_, code, _ = runArgv(ctx, r, o.wrap("dpkg", "-s", "unattended-upgrades"))
	add("auto-updates", code == 0, "unattended-upgrades is installed")

	// the spec's last word: prove the new path again, now against the
	// reloaded sshd
	if o.Prover == nil {
		add("mymo key access", false, "verify has no second-connection prover — the new path is unproven")
	} else if err := o.Prover.ProveMymoKey(ctx); err != nil {
		add("mymo key access", false, "the mymo key no longer works: "+err.Error())
	} else {
		add("mymo key access", true, "the key works on a fresh connection")
	}

	return checks, ok
}
