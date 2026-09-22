package cli

import (
	"strings"
	"testing"
)

func TestAppListNoAppsYet(t *testing.T) {
	s := newSession(t)
	code, out, _ := s.run(t, "app", "list")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "No applications") {
		t.Errorf("out = %q, want no-applications line", out)
	}
}

func TestAppRequiresSubcommand(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "app")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, "mymo app requires a subcommand") {
		t.Errorf("stderr = %q, want subcommand requirement", errStr)
	}
}

func TestAppUnknownCommand(t *testing.T) {
	s := newSession(t)
	code, _, errStr := s.run(t, "app", "bogus")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errStr, `unknown app command "bogus"`) {
		t.Errorf("stderr = %q, want unknown-command error", errStr)
	}
}
