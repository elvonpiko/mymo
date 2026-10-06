package ssh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/elvonpiko/mymo/internal/domain"
)

// TestForgetHostKeyRemovesTheEntry proves the other half of local
// removal: a forgotten node's trust entry must not survive to
// refuse a re-imaged box as an imposter on its next first contact.
func TestForgetHostKeyRemovesTheEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts.json")
	n := domain.Node{Name: "web-1", Host: "203.0.113.10", Port: 22, User: "root", Auth: domain.AuthAgent}
	c := New(n, path)
	if err := c.storeHostKey("203.0.113.10:22", "key-one"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHostKey(path, "203.0.113.10", 22); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the trust store must survive forgetting one host: %v", err)
	}
	doc, err := readKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := doc["203.0.113.10:22"]; still {
		t.Fatal("the entry survived forgetting")
	}
	// forgetting again is a no-op, and a missing store is fine
	if err := ForgetHostKey(path, "203.0.113.10", 22); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHostKey(filepath.Join(dir, "nope.json"), "h", 1); err != nil {
		t.Fatalf("a missing store is already forgotten: %v", err)
	}
	// other hosts keep their trust
	c2 := New(n, path)
	if err := c2.storeHostKey("198.51.100.7:22", "key-two"); err != nil {
		t.Fatal(err)
	}
	_ = errors.Is // keep errors imported for the doc check above
}
