package ssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "windows" {
		// unix: the key is 0600. windows protects it by the
		// profile's ACLs instead — mode bits say nothing there
		info, err := os.Stat(key)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("private key missing or too open: %v %v", err, info)
		}
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

// the pair mymo writes is the standard one: OpenSSH itself reads it.
// The interop is proven against the real ssh-keygen, not a mock —
// the Windows release must interoperate with the world's tools.
func TestGeneratedKeyInteroperatesWithOpenSSH(t *testing.T) {
	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("no ssh-keygen on this machine to prove interop with")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "node.key")
	pub, err := GenerateEd25519(key)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sshKeygen, "-y", "-f", key).Output()
	if err != nil {
		t.Fatalf("ssh-keygen cannot read mymo's private key: %v", err)
	}
	// ssh-keygen -y prints the key without the comment; the wire
	// form is what must match
	if got := strings.Fields(strings.TrimSpace(string(out))); got[1] != strings.Fields(pub)[1] {
		t.Errorf("ssh-keygen derived a different public half:\n%q\n%q", out, pub)
	}
	// and the fingerprint agrees, which is what a box's
	// authorized_keys comparison actually checks
	fp1, err := exec.Command(sshKeygen, "-l", "-f", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := exec.Command(sshKeygen, "-l", "-f", key+".pub").Output()
	if err != nil {
		t.Fatalf("ssh-keygen cannot read mymo's public key: %v", err)
	}
	if strings.Fields(string(fp1))[1] != strings.Fields(string(fp2))[1] {
		t.Errorf("fingerprints disagree:\n%s%s", fp1, fp2)
	}
}
