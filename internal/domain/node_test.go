package domain

import (
	"strings"
	"testing"
	"time"
)

func validNode() Node {
	return Node{
		Name:    "web-1",
		Host:    "203.0.113.10",
		Port:    DefaultSSHPort,
		User:    "root",
		Auth:    AuthKey,
		KeyPath: "/home/user/.ssh/id_ed25519",
		Mode:    ModeObserve,
		AddedAt: time.Now(),
	}
}

func TestValidateAcceptsValidNodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(n *Node)
	}{
		{"key auth", func(n *Node) {}},
		{"agent auth without key path", func(n *Node) { n.Auth = AuthAgent; n.KeyPath = "" }},
		{"single char name", func(n *Node) { n.Name = "x" }},
		{"max length name", func(n *Node) { n.Name = strings.Repeat("a", 40) }},
		{"app host mode", func(n *Node) { n.Mode = ModeAppHost }},
		{"non default port", func(n *Node) { n.Port = 2222 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := validNode()
			tc.mod(&n)
			if err := n.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestValidateRejectsInvalidNodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(n *Node)
		want string
	}{
		{"empty name", func(n *Node) { n.Name = "" }, "name"},
		{"uppercase name", func(n *Node) { n.Name = "Web-1" }, "name"},
		{"leading dash", func(n *Node) { n.Name = "-web" }, "name"},
		{"trailing dash", func(n *Node) { n.Name = "web-" }, "name"},
		{"leading digit", func(n *Node) { n.Name = "1web" }, "name"},
		{"underscore", func(n *Node) { n.Name = "web_1" }, "name"},
		{"too long name", func(n *Node) { n.Name = strings.Repeat("a", 41) }, "name"},
		{"empty host", func(n *Node) { n.Host = "" }, "host"},
		{"blank host", func(n *Node) { n.Host = "  " }, "host"},
		{"zero port", func(n *Node) { n.Port = 0 }, "port"},
		{"negative port", func(n *Node) { n.Port = -22 }, "port"},
		{"port too high", func(n *Node) { n.Port = 65536 }, "port"},
		{"empty user", func(n *Node) { n.User = "" }, "user"},
		{"empty auth", func(n *Node) { n.Auth = "" }, "auth"},
		{"unknown auth", func(n *Node) { n.Auth = "password" }, "auth"},
		{"key auth without key path", func(n *Node) { n.KeyPath = "" }, "key path"},
		{"empty mode", func(n *Node) { n.Mode = "" }, "mode"},
		{"unknown mode", func(n *Node) { n.Mode = "managed" }, "mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := validNode()
			tc.mod(&n)
			err := n.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestAddress(t *testing.T) {
	n := validNode()
	if got, want := n.Address(), "203.0.113.10:22"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}
	n.Port = 2222
	if got, want := n.Address(), "203.0.113.10:2222"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}
}
