package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
)

func testNode(name string) domain.Node {
	return domain.Node{
		Name:    name,
		Host:    "203.0.113.10",
		Port:    domain.DefaultSSHPort,
		User:    "root",
		Auth:    domain.AuthKey,
		KeyPath: "/home/user/.ssh/id_ed25519",
		Mode:    domain.ModeObserve,
		AddedAt: time.Now(),
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDir() = %v, want nil", err)
	}
	return s
}

func TestOpenDirCreatesStoreWithRestrictedPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mymo")
	if _, err := OpenDir(dir); err != nil {
		t.Fatalf("OpenDir() = %v, want nil", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != dirPerm {
		t.Fatalf("dir perms = %o, want %o", perm, dirPerm)
	}
}

func TestOpenDirRequiresPath(t *testing.T) {
	if _, err := OpenDir(""); err == nil {
		t.Fatal("OpenDir(\"\") = nil error, want error")
	}
}

func TestLoadNodesMissingFileIsEmpty(t *testing.T) {
	s := openTestStore(t)
	nodes, err := s.LoadNodes()
	if err != nil {
		t.Fatalf("LoadNodes() = %v, want nil", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("len(nodes) = %d, want 0", len(nodes))
	}
}

func TestAddNodeRoundTrip(t *testing.T) {
	s := openTestStore(t)
	want := testNode("web-1")
	if err := s.AddNode(want); err != nil {
		t.Fatalf("AddNode() = %v, want nil", err)
	}
	got, err := s.GetNode("web-1")
	if err != nil {
		t.Fatalf("GetNode() = %v, want nil", err)
	}
	if got.Name != want.Name || got.Host != want.Host || got.Port != want.Port ||
		got.User != want.User || got.Auth != want.Auth || got.KeyPath != want.KeyPath {
		t.Fatalf("round trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestAddNodeDuplicateName(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatalf("AddNode() = %v, want nil", err)
	}
	err := s.AddNode(testNode("web-1"))
	if !errors.Is(err, ErrNodeExists) {
		t.Fatalf("AddNode() duplicate = %v, want ErrNodeExists", err)
	}
}

func TestAddNodeRejectsInvalidNode(t *testing.T) {
	s := openTestStore(t)
	bad := testNode("BAD")
	if err := s.AddNode(bad); err == nil {
		t.Fatal("AddNode(invalid) = nil, want error")
	}
}

func TestGetNodeNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetNode("nope")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("GetNode() = %v, want ErrNodeNotFound", err)
	}
}

func TestUpdateNode(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatal(err)
	}
	updated := testNode("web-1")
	updated.Host = "203.0.113.99"
	if err := s.UpdateNode(updated); err != nil {
		t.Fatalf("UpdateNode() = %v, want nil", err)
	}
	got, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "203.0.113.99" {
		t.Fatalf("host = %q, want updated value", got.Host)
	}
}

func TestUpdateNodeNotFound(t *testing.T) {
	s := openTestStore(t)
	err := s.UpdateNode(testNode("ghost"))
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("UpdateNode() = %v, want ErrNodeNotFound", err)
	}
}

func TestNodeLastCheckRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	updated.LastCheck = domain.CheckState{At: at, Error: "unreachable: connection refused"}
	if err := s.UpdateNode(updated); err != nil {
		t.Fatalf("UpdateNode() = %v, want nil", err)
	}
	got, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCheck != (domain.CheckState{At: at, Error: "unreachable: connection refused"}) {
		t.Fatalf("last check round trip:\ngot  %+v", got.LastCheck)
	}

	// a recovering check clears the error and keeps no stale state
	got.LastCheck = domain.CheckState{At: at.Add(time.Minute)}
	if err := s.UpdateNode(got); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCheck.Error != "" || !got.LastCheck.At.Equal(at.Add(time.Minute)) {
		t.Fatalf("recovered check = %+v", got.LastCheck)
	}

	// a never-checked node carries no last_check key at all
	if err := s.AddNode(testNode("plain")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(), nodesFile))
	if err != nil {
		t.Fatal(err)
	}
	plainRaw := string(b)
	if idx := strings.Index(plainRaw, `"plain"`); idx >= 0 {
		if strings.Contains(plainRaw[idx:], "last_check") {
			t.Fatalf("never-checked node grew a last_check key:\n%s", b)
		}
	}
}

func TestNodeFactsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	want := facts.Node{
		Hostname:    "web-1.example",
		OS:          "Debian GNU/Linux 12 (bookworm)",
		Kernel:      "6.1.0-13-amd64",
		Arch:        "x86_64",
		CPUs:        8,
		Uptime:      42 * time.Hour,
		MemTotal:    8 * 1024 * 1024 * 1024,
		MemAvail:    4 * 1024 * 1024 * 1024,
		DiskTotal:   25 * 1024 * 1024 * 1024,
		DiskFree:    13 * 1024 * 1024 * 1024,
		Docker:      "Docker version 27.3.1, build 1234",
		Caddy:       "v2.8.4",
		Systemd:     true,
		User:        "root",
		CollectedAt: time.Now().Truncate(time.Second),
	}
	updated.Facts = want
	if err := s.UpdateNode(updated); err != nil {
		t.Fatalf("UpdateNode() = %v, want nil", err)
	}
	got, err := s.GetNode("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Facts != want {
		t.Fatalf("facts round trip mismatch:\ngot  %+v\nwant %+v", got.Facts, want)
	}
}

func TestNodeWithoutFactsOmitsField(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("plain")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(), nodesFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "facts") {
		t.Fatalf("nodes file grew a facts key for a never-probed node:\n%s", b)
	}
}

func TestDeleteNode(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNode(testNode("web-2")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode("web-1"); err != nil {
		t.Fatalf("DeleteNode() = %v, want nil", err)
	}
	nodes, err := s.LoadNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "web-2" {
		t.Fatalf("nodes after delete = %+v, want only web-2", nodes)
	}
	if err := s.DeleteNode("web-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("DeleteNode() missing = %v, want ErrNodeNotFound", err)
	}
}

func TestNodesFilePermissions(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddNode(testNode("web-1")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir(), nodesFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != filePerm {
		t.Fatalf("file perms = %o, want %o", perm, filePerm)
	}
}

func TestLoadNodesRejectsFutureSchema(t *testing.T) {
	s := openTestStore(t)
	b := []byte(`{"schema_version": 2, "nodes": []}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), nodesFile), b, filePerm); err != nil {
		t.Fatal(err)
	}
	_, err := s.LoadNodes()
	if !errors.Is(err, ErrSchemaFuture) {
		t.Fatalf("LoadNodes() = %v, want ErrSchemaFuture", err)
	}
}

func TestLoadNodesRejectsCorruptFile(t *testing.T) {
	s := openTestStore(t)
	b := []byte(`{"schema_version": 1,`)
	if err := os.WriteFile(filepath.Join(s.Dir(), nodesFile), b, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadNodes(); err == nil {
		t.Fatal("LoadNodes() = nil, want parse error")
	}
}
