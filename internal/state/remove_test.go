package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestRemoveNodeRefusesWhileAppsRemain proves applications gate the
// removal: the operator removes them one by one, deliberately.
func TestRemoveNodeRefusesWhileAppsRemain(t *testing.T) {
	s := OpenDirForTest(t)
	n := domain.Node{Name: "web-1", Host: "203.0.113.10", Port: 22,
		User: "root", Auth: domain.AuthAgent, Mode: domain.ModeAppHost, AddedAt: time.Now()}
	if err := s.AddNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.AddApp(domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage, Image: "nginx:1.27", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	err := s.RemoveNode("web-1")
	if !errors.Is(err, ErrNodeHasApps) {
		t.Fatalf("RemoveNode = %v, want ErrNodeHasApps", err)
	}
	if !strings.Contains(err.Error(), "api") {
		t.Fatalf("the refusal must name the applications: %v", err)
	}
	if _, err := s.GetNode("web-1"); err != nil {
		t.Fatal("a refused removal must leave the node in place")
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
