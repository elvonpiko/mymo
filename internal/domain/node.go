// Package domain holds mymo's core model types and the invariants that
// apply to them. It has no I/O dependencies.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/elvonpiko/mymo/internal/facts"
)

// DefaultSSHPort is the SSH port used when a node does not specify one.
const DefaultSSHPort = 22

// NodeMode describes how mymo relates to a node.
type NodeMode string

const (
	// ModeObserve means mymo may connect to and inspect the node, but
	// never mutates it.
	ModeObserve NodeMode = "observe"
	// ModeAppHost means the user explicitly prepared the node as a mymo
	// application host; mymo then owns a documented subset of it.
	ModeAppHost NodeMode = "app-host"
)

// AuthMethod describes how mymo authenticates SSH connections to a node.
type AuthMethod string

const (
	// AuthKey uses the private key file at KeyPath.
	AuthKey AuthMethod = "key"
	// AuthAgent uses the local SSH agent.
	AuthAgent AuthMethod = "agent"
)

// CheckState is the outcome of the most recent discovery probe attempt,
// successful or not. Error is empty exactly when the attempt succeeded.
type CheckState struct {
	At    time.Time `json:"at,omitzero"`
	Error string    `json:"error,omitempty"`
}

// Node is a remote server known to mymo, stored in local state.
type Node struct {
	Name    string     `json:"name"`
	Host    string     `json:"host"`
	Port    int        `json:"port"`
	User    string     `json:"user"`
	Auth    AuthMethod `json:"auth"`
	KeyPath string     `json:"key_path,omitempty"`
	Mode    NodeMode   `json:"mode"`
	AddedAt time.Time  `json:"added_at"`

	// Facts is the last discovery snapshot for this node, refreshed by
	// probes. CollectedAt doubles as the last-seen marker.
	Facts facts.Node `json:"facts,omitzero"`

	// LastCheck is the outcome of the most recent probe attempt. A
	// failed check keeps the last good facts while recording why the
	// attempt failed, so health never silently reverts to green.
	LastCheck CheckState `json:"last_check,omitzero"`
}

// nodeNamePattern allows 1-40 lowercase names built from letters, digits,
// and dashes. Names must start with a letter and end alphanumeric, so
// names stay valid shell words and valid DNS labels.
var nodeNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidateNodeName reports whether name is a valid node name.
func ValidateNodeName(name string) error {
	if !nodeNamePattern.MatchString(name) {
		return errors.New("name must be 1-40 chars: lowercase letters, digits, dashes; must start with a letter and end with a letter or digit")
	}
	return nil
}

// Validate reports whether the node record is internally consistent.
// It does not perform I/O (key file existence is checked by callers).
func (n Node) Validate() error {
	if err := ValidateNodeName(n.Name); err != nil {
		return fmt.Errorf("invalid name %q: %w", n.Name, err)
	}
	if strings.TrimSpace(n.Host) == "" {
		return errors.New("host is required")
	}
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", n.Port)
	}
	if strings.TrimSpace(n.User) == "" {
		return errors.New("user is required")
	}
	switch n.Auth {
	case AuthKey:
		if strings.TrimSpace(n.KeyPath) == "" {
			return fmt.Errorf("key path is required when auth is %q", AuthKey)
		}
	case AuthAgent:
	default:
		return fmt.Errorf("auth must be %q or %q, got %q", AuthKey, AuthAgent, n.Auth)
	}
	switch n.Mode {
	case ModeObserve, ModeAppHost:
	default:
		return fmt.Errorf("mode must be %q or %q, got %q", ModeObserve, ModeAppHost, n.Mode)
	}
	return nil
}

// Address returns the SSH endpoint in "host:port" form.
func (n Node) Address() string {
	return fmt.Sprintf("%s:%d", n.Host, n.Port)
}
