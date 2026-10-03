package cli

import (
	"strings"
	"testing"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
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

func TestAppListPrintsManagedApps(t *testing.T) {
	s := newSession(t)
	s.run(t, "app", "list") // empty store
	a := domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "ghcr.io/x/api:1", Port: 8080, Domain: "api.example.com",
		Releases: []domain.Release{{ID: 2, State: domain.ReleaseActive,
			Digest: "sha256:0123456789abcdef0123456789abcdef", Container: "mymo-api-r2"}}}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddApp(a); err != nil {
		t.Fatal(err)
	}
	code, out, _ := s.run(t, "app", "list")
	if code != exitOK {
		t.Fatalf("app list failed")
	}
	for _, want := range []string{"api", "web-1", "api.example.com", "release 2", "active", "sha256:0123456789abcde"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	// internal-only apps say so — the exposure column never lies
	a2 := domain.App{Name: "worker", Node: "web-1", Type: domain.SourceDockerfile,
		Dockerfile: ".", Port: 3000}
	a2.Releases = nil
	if err := store.AddApp(a2); err != nil {
		t.Fatal(err)
	}
	_, out, _ = s.run(t, "app", "list")
	if !strings.Contains(out, "internal only") {
		t.Errorf("internal app not labeled:\n%s", out)
	}
	if !strings.Contains(out, "no releases yet") {
		t.Error("the app without releases should say so")
	}
}
