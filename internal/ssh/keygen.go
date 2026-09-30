package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GenerateEd25519 creates the mymo key pair for one node, stored under
// the local state directory so the private half never leaves the
// operator's machine. An existing key is reused — a re-apply must not
// churn the node's authorized_keys, and a reused key keeps the second
// connection honest across applies.
func GenerateEd25519(path string) (string, error) {
	// an existing pair is reused as-is
	if pub, err := os.ReadFile(path + ".pub"); err == nil {
		if s := strings.TrimSpace(string(pub)); strings.HasPrefix(s, "ssh-ed25519 ") {
			return s, nil
		}
	}
	// private key without its .pub: derive the public half from it
	if _, err := os.Stat(path); err == nil {
		out, err := exec.Command("ssh-keygen", "-y", "-f", path).Output()
		if err != nil {
			return "", fmt.Errorf("reading the existing key at %s: %w", path, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "mymo "+filepath.Base(path), "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %s", strings.TrimSpace(string(out)))
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", fmt.Errorf("ssh-keygen wrote no public key at %s.pub", path)
	}
	s := strings.TrimSpace(string(pub))
	if !strings.HasPrefix(s, "ssh-ed25519 ") {
		return "", fmt.Errorf("ssh-keygen wrote an unexpected key type at %s.pub", path)
	}
	return s, nil
}
