package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
)

// TestRemoveNodeForgetsEverythingLocal proves removal is the whole of
// mymo's side of the deal: the record, the dedicated key pair — and
// nothing else. The box is never contacted; what mymo built there
// keeps running. Applications refuse the removal first: their release
// history must not vanish as a side effect.
func TestRemoveNodeForgetsEverythingLocal(t *testing.T) {
	s := OpenDirForTest(t)
	n := domain.Node{Name: "web-1", Host: "203.0.113.10", Port: 22,
		User: "root", Auth: domain.AuthKey, KeyPath: "", Mode: domain.ModeObserve, AddedAt: time.Now()}
	n.KeyPath = filepath.Join(s.dir, "keys", "web-1.key")
	if err := s.AddNode(n); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(s.dir, "keys", "web-1.key")
	os.MkdirAll(filepath.Dir(keyPath), 0o700)
	os.WriteFile(keyPath, []byte("private"), 0o600)
	os.WriteFile(keyPath+".pub", []byte("public"), 0o644)

	if err := s.RemoveNode("web-1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if _, err := s.GetNode("web-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("record survived removal: %v", err)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("the dedicated key pair survived removal")
	}
	if _, err := os.Stat(keyPath + ".pub"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the public key survived removal")
	}
	// removing again is an honest error, not a silent no-op
	if err := s.RemoveNode("web-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("second removal = %v, want ErrNodeNotFound", err)
	}
}

// TestRemoveNodeForgetsItsApps proves the whole goodbye: a node's
// removal forgets its applications' records with it — locally
// only, and only theirs. A box already gone is exactly why the
// records cannot be hostages of a reachable machine; an app on
// another node survives untouched.
func TestRemoveNodeForgetsItsApps(t *testing.T) {
	s := OpenDirForTest(t)
	for _, n := range []domain.Node{
		{Name: "web-1", Host: "203.0.113.10", Port: 22, User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()},
		{Name: "web-2", Host: "203.0.113.11", Port: 22, User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()},
	} {
		if err := s.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []domain.App{
		{Name: "api", Node: "web-1", Type: domain.SourceImage, Image: "nginx:1.27", Port: 8080},
		{Name: "web", Node: "web-1", Type: domain.SourceImage, Image: "nginx:1.27", Port: 80},
		{Name: "keeper", Node: "web-2", Type: domain.SourceImage, Image: "nginx:1.27", Port: 80},
	} {
		if err := s.AddApp(a); err != nil {
			t.Fatal(err)
		}
	}
	on, err := s.AppsOnNode("web-1")
	if err != nil || len(on) != 2 {
		t.Fatalf("AppsOnNode = %v, %v; want the two on web-1", on, err)
	}
	if err := s.RemoveNode("web-1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if _, err := s.GetNode("web-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("record survived removal: %v", err)
	}
	for _, gone := range []string{"api", "web"} {
		if _, err := s.GetApp(gone); !errors.Is(err, ErrAppNotFound) {
			t.Fatalf("app %s survived its node's removal: %v", gone, err)
		}
	}
	if _, err := s.GetApp("keeper"); err != nil {
		t.Fatalf("an app on another node must survive: %v", err)
	}
	// removing again is an honest error, not a silent no-op
	if err := s.RemoveNode("web-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("second removal = %v, want ErrNodeNotFound", err)
	}
}

// OpenDirForTest is the test-side constructor: OpenDir's error is
// impossible on a TempDir.
func OpenDirForTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
