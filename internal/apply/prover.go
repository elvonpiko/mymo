package apply

import (
	"context"
	"fmt"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// SSHProver proves the mymo key the way it will be used after the
// reload: a real second connection, dialed fresh, authenticated with
// the generated key against the same host key the operator already
// trusts. The operator's own session stays open the whole time.
type SSHProver struct {
	// Node is the operator's node record: host and port are reused,
	// the identity is replaced with the mymo user.
	Node       domain.Node
	PrivateKey string // the locally generated mymo key
	KnownHosts string // the store's known_hosts file
}

// ProveMymoKey dials as mymo with the generated key and runs the one
// command that proves the identity end to end.
func (p SSHProver) ProveMymoKey(ctx context.Context) error {
	n := p.Node
	n.User = MymoKeyUser
	n.Auth = domain.AuthKey
	n.KeyPath = p.PrivateKey
	c := ssh.New(n, p.KnownHosts)
	if err := c.Dial(ctx); err != nil {
		return fmt.Errorf("second connection as %s: %w", MymoKeyUser, err)
	}
	defer c.Close()
	out, code, err := c.Run(ctx, "id", "-un")
	if err != nil || code != 0 || out != MymoKeyUser {
		return fmt.Errorf("second connection as %s: the key was accepted but the identity check failed", MymoKeyUser)
	}
	return nil
}
