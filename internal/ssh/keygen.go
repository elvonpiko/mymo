package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateEd25519 creates the mymo key pair for one node, stored under
// the local state directory so the private half never leaves the
// operator's machine. The pair is generated and read in pure Go —
// mymo carries no dependency on a local ssh-keygen, because the
// operator's machine may be one that has none. The formats are the
// standard ones: an OpenSSH private key, an authorized_keys line.
//
// An existing key is reused — a re-apply must not churn the node's
// authorized_keys, and a reused key keeps the second connection
// honest across applies.
func GenerateEd25519(path string) (string, error) {
	comment := "mymo " + filepath.Base(path)

	// an existing pair is reused as-is
	if pub, err := os.ReadFile(path + ".pub"); err == nil {
		if s := strings.TrimSpace(string(pub)); strings.HasPrefix(s, "ssh-ed25519 ") {
			return s, nil
		}
	}
	// private key without its .pub: the public half is derived from
	// the private one, the way ssh-keygen -y would
	if raw, err := os.ReadFile(path); err == nil {
		signer, perr := ssh.ParsePrivateKey(raw)
		if perr != nil {
			return "", fmt.Errorf("reading the existing key at %s: %w", path, perr)
		}
		return publicLine(signer.PublicKey(), comment), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	line := publicLine(mustPublicKey(pub), comment)
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// publicLine renders one authorized_keys line: the key's type, its
// wire form, and mymo's comment naming the node it belongs to.
func publicLine(key ssh.PublicKey, comment string) string {
	line := strings.TrimRight(string(ssh.MarshalAuthorizedKey(key)), "\n")
	if comment != "" {
		line += " " + comment
	}
	return line
}

// mustPublicKey cannot fail for a freshly generated ed25519 key;
// the transport reads exactly what was just written.
func mustPublicKey(pub ed25519.PublicKey) ssh.PublicKey {
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic("an ed25519 public key that cannot be encoded: " + err.Error())
	}
	return key
}
