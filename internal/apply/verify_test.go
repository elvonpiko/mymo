package apply

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/baseline"
)

// hardenedPosture renders the effective sshd -T output the baseline
// expects, built from the baseline's own directives.
func hardenedPosture() string {
	var b strings.Builder
	for _, d := range baseline.SSHDirectives {
		fmt.Fprintf(&b, "%s %s\n", strings.ToLower(d.Key), d.Value)
	}
	b.WriteString("port 22\n")
	return b.String()
}

func verifyRunner(respond func(argv []string) (string, int, error)) *fakeRunner {
	r := &fakeRunner{}
	r.respond = func(argv []string) (string, int, error) {
		// a test's override wins whenever it has something to say
		if respond != nil {
			if out, code, err := respond(argv); err != nil || code != 0 || out != "" {
				return out, code, err
			}
		}
		if argv[0] == "sshd" {
			return hardenedPosture(), 0, nil
		}
		if argv[0] == "ufw" {
			return "Status: active\n", 0, nil
		}
		if argv[0] == "id" {
			return "uid=1000(mymo)", 0, nil
		}
		if argv[0] == "cat" {
			return "{\n  \"baseline\": \"0.1\",\n  \"appliedAt\": \"2026-02-15T10:00:00Z\"\n}\n", 0, nil
		}
		if argv[0] == "systemctl" {
			return "enabled", 0, nil
		}
		if respond != nil {
			return respond(argv)
		}
		return "", 0, nil
	}
	return r
}

func TestVerifyPassesOnAHardenedNode(t *testing.T) {
	r := verifyRunner(nil)
	checks, ok := Verify(context.Background(), r, testOptions(r, &fakeProver{r: r}), "0.1")
	if !ok {
		for _, c := range checks {
			t.Logf("%-18s %s: %s", c.Title, c.Outcome, c.Detail)
		}
		t.Fatal("verify failed on a fully hardened node")
	}
	for _, c := range checks {
		if c.Group != "verify" {
			t.Errorf("check %q group = %q", c.Title, c.Group)
		}
	}
}

func TestVerifyFailsWhenThePostureLies(t *testing.T) {
	r := verifyRunner(func(argv []string) (string, int, error) {
		if argv[0] == "sshd" {
			return strings.Replace(hardenedPosture(), "passwordauthentication no", "passwordauthentication yes", 1), 0, nil
		}
		return "", 0, nil
	})
	checks, ok := Verify(context.Background(), r, testOptions(r, &fakeProver{r: r}), "0.1")
	if ok {
		t.Fatal("verify passed while password auth is still on")
	}
	found := false
	for _, c := range checks {
		if strings.Contains(c.Detail, "PasswordAuthentication") {
			found = true
		}
	}
	if !found {
		t.Error("no check named the lying directive")
	}
}

func TestVerifyReprovesTheKey(t *testing.T) {
	r := verifyRunner(nil)
	_, ok := Verify(context.Background(), r, testOptions(r, &fakeProver{r: r, err: fmt.Errorf("rejected")}), "0.1")
	if ok {
		t.Fatal("verify passed with a broken mymo key")
	}

	r2 := verifyRunner(nil)
	_, ok = Verify(context.Background(), r2, testOptions(r2, nil), "0.1")
	if ok {
		t.Fatal("verify passed without a prover — the new path must be proven again after the reload")
	}
}

func TestVerifyFailsWithoutTheMarker(t *testing.T) {
	r := verifyRunner(func(argv []string) (string, int, error) {
		if argv[0] == "cat" {
			return "", 1, fmt.Errorf("absent")
		}
		return "", 0, nil
	})
	checks, ok := Verify(context.Background(), r, testOptions(r, &fakeProver{r: r}), "0.1")
	if ok {
		t.Fatal("verify passed without the baseline marker")
	}
	for _, c := range checks {
		if c.Title == "baseline marker" && c.Outcome != 0 {
			return
		}
	}
	t.Error("the marker check did not fail")
}

func TestVerifyRejectsAPlaceholderStamp(t *testing.T) {
	r := verifyRunner(func(argv []string) (string, int, error) {
		if argv[0] == "cat" {
			return "{\n  \"baseline\": \"0.1\",\n  \"appliedAt\": \"<at apply>\"\n}\n", 0, nil
		}
		return "", 0, nil
	})
	_, ok := Verify(context.Background(), r, testOptions(r, &fakeProver{r: r}), "0.1")
	if ok {
		t.Fatal("verify passed on a marker that was never stamped")
	}
}
