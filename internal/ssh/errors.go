package ssh

import "errors"

// Transport errors are structured so callers can tell operational
// failures (retry, fix, ask) from refusals (never guess). Every wrap
// keeps the underlying detail in the message.
var (
	// ErrUnreachable reports a transport-level failure to reach the
	// node: refused, unroutable, or timed out mid-handshake.
	ErrUnreachable = errors.New("unreachable")

	// ErrAuth reports that the node refused the configured
	// credentials.
	ErrAuth = errors.New("authentication failed")

	// ErrHostKeyMismatch reports that the node's host key no longer
	// matches the one recorded on first contact — a possible
	// man-in-the-middle. The connection is refused, never overridden.
	ErrHostKeyMismatch = errors.New("host key mismatch")

	// ErrNoAgent reports that the SSH agent is unavailable or holds
	// no usable keys.
	ErrNoAgent = errors.New("ssh agent unavailable")

	// ErrKeyRejected reports that the configured key file could not
	// be read or parsed.
	ErrKeyRejected = errors.New("ssh key unusable")
)
