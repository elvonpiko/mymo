// firstcontact is the fresh-box onboarding: a provider hands you an
// IP, a user, and a password, and mymo turns that into a key-auth
// node on file — generate a dedicated key, connect once with the
// password, append the key's public half to authorized_keys
// (append-only, never rewriting the file), and prove the key works
// on a second connection before anything is saved. The password
// exists only in the memory of the one client that uses it; no
// persisted record in mymo has a field that could hold it.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/elvonpiko/mymo/internal/domain"
)

// ErrPasswordExpired reports the box's forced password reset: fresh
// provider images demand a change before any command runs, and first
// contact cannot install the key until the operator has made it. The
// move stays theirs — mymo never rotates a credential it does not
// own; it says the condition plainly instead of spewing the box's
// refusal as a nameless exit 1.
var ErrPasswordExpired = errors.New("the node demands a password change before first contact")

// expiredSignatures are what sshd and its MOTD print when the
// account is in the forced-reset state.
var expiredSignatures = []string{
	"password has expired",
	"required to change your password",
	"must change your password",
}

// isExpiredPassword tells a forced reset from an ordinary failure by
// the box's own words.
func isExpiredPassword(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range expiredSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// FirstContact runs the whole password-to-key onboarding for one
// node. keyPath is where the dedicated pair lives (the caller
// passes the store's keys directory); knownHostsPath is the trust
// store the connection records first contact into. progress, when
// not nil, receives the connect retries and the steps between them
// — the operator sees a booting box as "still trying", never as a
// hang. It returns nil only when the key has been proven on a real
// second connection.
func FirstContact(ctx context.Context, host string, port int, user, password, keyPath, knownHostsPath string, progress func(string)) error {
	// 1 — the dedicated key, generated locally, reused as-is when
	// first contact is retried
	pub, err := GenerateEd25519(keyPath)
	if err != nil {
		return fmt.Errorf("generating the dedicated key: %w", err)
	}

	// 2 — the one password-authenticated connection: install the
	// public half, then the password is never used again
	install := NewFirstContact(domain.Node{Host: host, Port: port, User: user}, knownHostsPath, password)
	install.WithProgress(progress)
	if err := install.Dial(ctx); err != nil {
		if isExpiredPassword(err) {
			return fmt.Errorf("%w: ssh in once, change the password, then run first contact again — it is idempotent", ErrPasswordExpired)
		}
		return err
	}
	if progress != nil {
		progress("connected to " + host + " — installing the key")
	}
	defer install.Close()
	if err := install.AppendAuthorizedKey(ctx, pub); err != nil {
		if isExpiredPassword(err) {
			return fmt.Errorf("%w: ssh in once, change the password, then run first contact again — it is idempotent", ErrPasswordExpired)
		}
		return err
	}

	// 3 — the proof: a second connection, this time with the key
	// the node now trusts. Nothing is saved until this works.
	verify := New(domain.Node{Host: host, Port: port, User: user,
		Auth: domain.AuthKey, KeyPath: keyPath}, knownHostsPath)
	verify.WithProgress(progress)
	if err := verify.Dial(ctx); err != nil {
		return fmt.Errorf("the key was installed but does not authenticate: %w", err)
	}
	defer verify.Close()
	out, _, err := verify.Run(ctx, "whoami")
	if err != nil {
		return fmt.Errorf("the key authenticated but the node did not answer: %w", err)
	}
	if who := strings.TrimSpace(out); who != user {
		return fmt.Errorf("the key logs in as %q, not %q — the node was not saved", who, user)
	}
	return nil
}

// FirstContactKeyPath places a node's dedicated key inside the
// state store's keys directory, beside the mymo-user keys the
// bootstrap generates.
func FirstContactKeyPath(stateDir, node string) string {
	return filepath.Join(stateDir, "keys", node+".key")
}
