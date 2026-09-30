package apply

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/plan"
)

// fakeRunner records every command and answers from canned responses.
type fakeRunner struct {
	mu       sync.Mutex
	commands [][]string
	respond  func(argv []string) (string, int, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := append([]string{name}, args...)
	f.commands = append(f.commands, argv)
	if f.respond != nil {
		return f.respond(argv)
	}
	return "", 0, nil
}

func (f *fakeRunner) flat() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.commands))
	for _, c := range f.commands {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func (f *fakeRunner) has(cmd string) bool {
	for _, c := range f.flat() {
		if c == cmd {
			return true
		}
	}
	return false
}

// fakeProver records its call into the runner's stream so tests can
// assert it happened between the right commands.
type fakeProver struct {
	r   *fakeRunner
	err error
}

func (p *fakeProver) ProveMymoKey(context.Context) error {
	p.r.mu.Lock()
	p.r.commands = append(p.r.commands, []string{"PROVER"})
	p.r.mu.Unlock()
	return p.err
}

func testSteps() []plan.Step {
	return []plan.Step{
		{Control: "update", Title: "system update",
			Exec: [][]string{{"apt-get", "update"}}},
		{Control: "mymo-user", Title: "mymo admin user",
			Files: []plan.File{
				{Path: "/etc/sudoers.d/mymo", Content: "mymo ALL=(ALL) NOPASSWD:ALL\n", Mode: "0440", Owner: "root:root"},
			},
			Exec: [][]string{
				{"useradd", "-m", "-s", "/bin/bash", "mymo"},
				{"sh", "-c", "printf '%s\\n' '<mymo-public-key>' > /home/mymo/.ssh/authorized_keys"},
			}},
		{Control: "ssh-hardening", Title: "sshd hardening",
			Files: []plan.File{
				{Path: "/etc/ssh/sshd_config.d/99-mymo.conf", Content: "PermitRootLogin no\nPasswordAuthentication no\n", Mode: "0644", Owner: "root:root"},
			},
			Exec: [][]string{
				{"sshd", "-t"},
				{"systemctl", "reload", "ssh"},
			},
			Gate: "second connection"},
	}
}

func testOptions(r *fakeRunner, prover Prover) Options {
	return Options{
		MymoPublicKey: "ssh-ed25519 AAAAC3Nza test-key comment",
		Now:           time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC),
		Prover:        prover,
		Progress:      func(string) {},
	}
}

func TestApplyRunsEveryStepInOrder(t *testing.T) {
	r := &fakeRunner{}
	res := Apply(context.Background(), r, testSteps(), testOptions(r, &fakeProver{r: r}))
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	for i, s := range res.Steps {
		if s.State != StepDone {
			t.Errorf("step %d (%s) state = %q, want done", i, s.Control, s.State)
		}
	}
	flat := strings.Join(r.flat(), "\n")
	want := []string{
		"apt-get update",
		"id mymo", // the useradd pre-check
		"useradd -m -s /bin/bash mymo",
		"cat /etc/sudoers.d/mymo",
		"sshd -t",
		"PROVER",
		"systemctl reload ssh",
	}
	for _, w := range want {
		if !strings.Contains(flat, w) {
			t.Errorf("missing command %q in:\n%s", w, flat)
		}
	}
}

func TestApplyStopsAtFirstFailure(t *testing.T) {
	r := &fakeRunner{respond: func(argv []string) (string, int, error) {
		if argv[0] == "apt-get" {
			return "no network", 100, fmt.Errorf("exit 100")
		}
		return "", 0, nil
	}}
	res := Apply(context.Background(), r, testSteps(), testOptions(r, &fakeProver{r: r}))
	if !res.Failed || res.FailedAt != "update" {
		t.Fatalf("apply should fail at update: failed=%v at=%q", res.Failed, res.FailedAt)
	}
	if res.Steps[0].State != StepFailed || !strings.Contains(res.Steps[0].Note, "apt-get update failed") {
		t.Errorf("step 0 note = %q", res.Steps[0].Note)
	}
	if res.Steps[1].State != StepBlocked || res.Steps[2].State != StepBlocked {
		t.Error("steps after the failure must be blocked, never attempted")
	}
	if r.has("useradd -m -s /bin/bash mymo") || r.has("systemctl reload ssh") {
		t.Error("commands after the failure ran anyway")
	}
}

func TestGateProvesTheKeyBeforeTheReload(t *testing.T) {
	r := &fakeRunner{}
	res := Apply(context.Background(), r, testSteps(), testOptions(r, &fakeProver{r: r}))
	if !res.GateProven {
		t.Fatal("the gate should have proven the key")
	}
	flat := r.flat()
	var validate, prover, reload int
	for i, c := range flat {
		if c == "sshd -t" && validate == 0 {
			validate = i
		}
		if c == "PROVER" && prover == 0 {
			prover = i
		}
		if c == "systemctl reload ssh" {
			reload = i
		}
	}
	if !(validate < prover && prover < reload) {
		t.Fatalf("gate order broken: sshd -t at %d, proof at %d, reload at %d\n%s", validate, prover, reload, strings.Join(r.flat(), "\n"))
	}
}

func TestGateFailureNeverReloads(t *testing.T) {
	r := &fakeRunner{}
	res := Apply(context.Background(), r, testSteps(), testOptions(r, &fakeProver{r: r, err: fmt.Errorf("key rejected")}))
	if !res.Failed || res.FailedAt != "ssh-hardening" {
		t.Fatalf("apply should fail at the gate: failed=%v at=%q", res.Failed, res.FailedAt)
	}
	if res.GateProven {
		t.Error("a failed proof must not count as proven")
	}
	if r.has("systemctl reload ssh") {
		t.Fatal("the reload ran without a proven key — this is the lockout bug")
	}
	if !strings.Contains(res.Steps[2].Note, "untouched") {
		t.Errorf("the failure note must say the operator's access is untouched: %q", res.Steps[2].Note)
	}
}

func TestGatedStepNeedsItsCommitCommand(t *testing.T) {
	steps := []plan.Step{{
		Control: "ssh-hardening", Title: "sshd hardening",
		Exec: [][]string{{"systemctl", "reload", "ssh"}},
		Gate: "second connection",
	}}
	r := &fakeRunner{}
	res := Apply(context.Background(), r, steps, testOptions(r, &fakeProver{r: r}))
	if !res.Failed || !strings.Contains(res.Steps[0].Note, "commit command") {
		t.Fatalf("a gated step with no pre-commit commands must fail: %+v", res)
	}
	if r.has("systemctl reload ssh") {
		t.Error("the reload ran from a malformed gated step")
	}
}

func TestApplyRequiresAProverForGatedSteps(t *testing.T) {
	r := &fakeRunner{}
	o := testOptions(r, nil)
	res := Apply(context.Background(), r, testSteps(), o)
	if !res.Failed || res.FailedAt != "ssh-hardening" {
		t.Fatalf("a gated step without a prover must fail: %+v", res)
	}
	if r.has("systemctl reload ssh") {
		t.Error("the reload ran without a prover")
	}
}

func TestPlaceholdersNeverReachTheNode(t *testing.T) {
	steps := []plan.Step{{
		Control: "state-dir", Title: "mymo state",
		Files: []plan.File{{
			Path:    "/var/lib/mymo/baseline.json",
			Content: "{\n  \"baseline\": \"0.1\",\n  \"appliedAt\": \"<at apply>\"\n}\n",
			Mode:    "0644", Owner: "mymo:mymo",
		}},
		Exec: [][]string{{"sh", "-c", "printf '%s\\n' '<mymo-public-key>' > /home/mymo/.ssh/authorized_keys"}},
	}}
	r := &fakeRunner{}
	res := Apply(context.Background(), r, steps, testOptions(r, &fakeProver{r: r}))
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	flat := strings.Join(r.flat(), "\n")
	if strings.Contains(flat, "<mymo-public-key>") || strings.Contains(flat, "<at apply>") {
		t.Errorf("a placeholder reached the node:\n%s", flat)
	}
	if !strings.Contains(flat, "ssh-ed25519 AAAAC3Nza test-key comment") {
		t.Error("the real public key was not substituted")
	}
	// the marker's content must carry the real timestamp
	for _, c := range r.commands {
		if c[0] == "sh" {
			parts := strings.Fields(c[len(c)-1])
			if len(parts) > 3 {
				raw, err := base64.StdEncoding.DecodeString(parts[2])
				if err == nil && strings.Contains(string(raw), "2026-02-15T10:00:00Z") {
					return
				}
			}
		}
	}
	t.Errorf("the marker was not stamped with the apply time:\n%s", flat)
}

func TestIdenticalFileIsKept(t *testing.T) {
	steps := []plan.Step{{
		Control: "sysctl", Title: "kernel flags",
		Files: []plan.File{{
			Path: "/etc/sysctl.d/99-mymo.conf", Content: "net.ipv4.ip_forward=1\n", Mode: "0644", Owner: "root:root",
		}},
	}}
	r := &fakeRunner{respond: func(argv []string) (string, int, error) {
		if argv[0] == "cat" {
			return "net.ipv4.ip_forward=1\n", 0, nil
		}
		return "", 0, nil
	}}
	res := Apply(context.Background(), r, steps, testOptions(r, nil))
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	if r.has("sh -c") {
		t.Error("an identical file was rewritten anyway")
	}
	if res.Steps[0].State != StepKept {
		t.Errorf("state = %q, want kept", res.Steps[0].State)
	}
}

func TestDifferentFileIsBackedUpThenWritten(t *testing.T) {
	steps := []plan.Step{{
		Control: "docker", Title: "docker",
		Files: []plan.File{{
			Path: "/etc/docker/daemon.json", Content: "{\"log-driver\":\"local\"}\n", Mode: "0644", Owner: "root:root",
		}},
	}}
	r := &fakeRunner{respond: func(argv []string) (string, int, error) {
		switch argv[0] {
		case "cat":
			return "{\"log-driver\":\"json-file\"}\n", 0, nil
		case "test":
			return "", 1, fmt.Errorf("no backup yet") // cp must run
		}
		return "", 0, nil
	}}
	res := Apply(context.Background(), r, steps, testOptions(r, nil))
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	if !r.has("cp /etc/docker/daemon.json /etc/docker/daemon.json.mymo-bak") {
		t.Error("the existing file was not backed up before being replaced")
	}
	for _, c := range r.commands {
		if c[0] == "sh" {
			parts := strings.Fields(c[len(c)-1])
			if len(parts) > 3 {
				raw, _ := base64.StdEncoding.DecodeString(parts[2])
				if !strings.Contains(string(raw), "local") {
					t.Errorf("written content = %q", string(raw))
				}
			}
		}
	}
}

func TestOriginalBackupIsNeverClobbered(t *testing.T) {
	steps := []plan.Step{{
		Control: "docker", Title: "docker",
		Files: []plan.File{{
			Path: "/etc/docker/daemon.json", Content: "{\"log-driver\":\"local\"}\n", Mode: "0644", Owner: "root:root",
		}},
	}}
	r := &fakeRunner{respond: func(argv []string) (string, int, error) {
		switch argv[0] {
		case "cat":
			return "{\"log-driver\":\"json-file\"}\n", 0, nil
		case "test":
			return "", 0, nil // a backup already exists
		}
		return "", 0, nil
	}}
	Apply(context.Background(), r, steps, testOptions(r, nil))
	if r.has("cp /etc/docker/daemon.json /etc/docker/daemon.json.mymo-bak") {
		t.Error("an existing backup was overwritten — the original must be preserved across re-runs")
	}
}

func TestExistingUserIsKept(t *testing.T) {
	r := &fakeRunner{respond: func(argv []string) (string, int, error) {
		if argv[0] == "id" {
			return "uid=1000(mymo)", 0, nil
		}
		return "", 0, nil
	}}
	res := Apply(context.Background(), r, testSteps(), testOptions(r, &fakeProver{r: r}))
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	if r.has("useradd -m -s /bin/bash mymo") {
		t.Error("useradd re-ran for an existing user")
	}
	anySh := false
	for _, c := range r.commands {
		if c[0] == "sh" {
			anySh = true
		}
	}
	if !anySh {
		t.Error("the rest of the step must still run")
	}
}

func TestSudoPrefixesEveryCommand(t *testing.T) {
	r := &fakeRunner{}
	Apply(context.Background(), r, testSteps(), func() Options {
		o := testOptions(r, &fakeProver{r: r})
		o.Sudo = true
		return o
	}())
	for _, c := range r.commands {
		if c[0] == "PROVER" {
			continue // the test's sentinel, not a remote command
		}
		if c[0] != "sudo" || c[1] != "-n" {
			t.Errorf("command not run through sudo -n: %v", c)
		}
	}
}
