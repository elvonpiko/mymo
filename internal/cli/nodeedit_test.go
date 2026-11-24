package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/state"
)

func TestNodeEditChangesTheAddressAndKeepsTheRest(t *testing.T) {
	s := newSession(t)
	code, out, errb := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent")
	if code != exitOK {
		t.Fatalf("add: code=%d out=%q err=%q", code, out, errb)
	}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	n.Mode = domain.ModeAppHost
	n.Bootstrap = domain.Bootstrap{State: domain.BootstrapReady, Baseline: "0.1", Verdict: "ready", At: time.Now()}
	if err := store.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	if err := store.AddApp(domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage, Image: "nginx:1.27", Port: 8080}); err != nil {
		t.Fatal(err)
	}

	code, out, errb = s.run(t, "node", "edit", "web-1", "-host", "198.51.100.8", "-user", "mymo")
	if code != exitOK {
		t.Fatalf("edit: code=%d out=%q err=%q", code, out, errb)
	}
	if !strings.Contains(out, "updated web-1") || !strings.Contains(out, "unchanged") {
		t.Fatalf("out = %q", out)
	}
	got, err := store.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "198.51.100.8" || got.User != "mymo" || got.Name != "web-1" {
		t.Fatalf("connection = %s %s@%s", got.Name, got.User, got.Host)
	}
	if got.Mode != domain.ModeAppHost || got.Bootstrap.State != domain.BootstrapReady || got.Bootstrap.Baseline != "0.1" {
		t.Fatalf("record changed underneath the edit: mode=%s bootstrap=%+v", got.Mode, got.Bootstrap)
	}
	apps, err := store.AppsOnNode("web-1")
	if err != nil || len(apps) != 1 || apps[0].Name != "api" {
		t.Fatalf("apps = %+v err=%v", apps, err)
	}
}

func TestNodeEditUnchangedIsHonest(t *testing.T) {
	s := newSession(t)
	if code, _, errb := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent"); code != exitOK {
		t.Fatal(errb)
	}
	code, out, errb := s.run(t, "node", "edit", "web-1", "-host", "203.0.113.10")
	if code != exitOK || !strings.Contains(out, "unchanged") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errb)
	}
}

func TestNodeEditRejectsPasswordAuth(t *testing.T) {
	s := newSession(t)
	if code, _, errb := s.run(t, "node", "add",
		"-name", "web-1", "-host", "203.0.113.10", "-user", "root", "-auth", "agent"); code != exitOK {
		t.Fatal(errb)
	}
	code, _, errb := s.run(t, "node", "edit", "web-1", "-auth", "password")
	if code != exitErr || !strings.Contains(errb, "auth") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}
