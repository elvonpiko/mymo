package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateEd25519CreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "keys", "web-1.key")

	pub, err := GenerateEd25519(key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("unexpected public key: %q", pub)
	}
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("private key missing or too open: %v %v", err, info)
	}

	// a second apply reuses the pair — authorized_keys never churns
	again, err := GenerateEd25519(key)
	if err != nil {
		t.Fatal(err)
	}
	if again != pub {
		t.Error("an existing key was regenerated instead of reused")
	}

	// a lost .pub is derived back from the private half
	if err := os.Remove(key + ".pub"); err != nil {
		t.Fatal(err)
	}
	derived, err := GenerateEd25519(key)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Errorf("derived key differs: %q vs %q", derived, pub)
	}
}
