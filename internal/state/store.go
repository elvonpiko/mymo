// Package state implements mymo's local state store: versioned JSON files
// under ~/.mymo, written atomically with restrictive permissions. No
// secrets are ever stored.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elvonpiko/mymo/internal/domain"
)

// SchemaVersion is the state format version this build reads and writes.
// Files with a newer schema are refused rather than guessed at.
const SchemaVersion = 1

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600

	nodesFile = "nodes.json"
)

// App store errors.
var (
	// ErrAppNotFound is returned when no app with the given name exists.
	ErrAppNotFound = errors.New("app not found")
	// ErrAppExists is returned when adding an app whose name is taken.
	ErrAppExists = errors.New("app already exists")
)

// Store errors.
var (
	// ErrNodeNotFound is returned when no node with the given name exists.
	ErrNodeNotFound = errors.New("node not found")
	// ErrNodeExists is returned when adding a node whose name is taken.
	ErrNodeExists = errors.New("node already exists")
	// ErrSchemaFuture is returned when state was written by a newer mymo.
	ErrSchemaFuture = errors.New("state written by a newer mymo version")
)

// Store is mymo's local state store rooted at a directory.
type Store struct {
	dir string
}

// DefaultDir returns the default state directory, ~/.mymo.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".mymo"), nil
}

// Open opens the default store, creating its directory when missing.
func Open() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return OpenDir(dir)
}

// OpenDir opens the store at dir, creating the directory when missing.
func OpenDir(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("state directory is required")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// nodesDoc is the on-disk format of nodes.json.
type nodesDoc struct {
	SchemaVersion int           `json:"schema_version"`
	Nodes         []domain.Node `json:"nodes"`
}

// LoadNodes returns all known nodes. A missing file is an empty list.
func (s *Store) LoadNodes() ([]domain.Node, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, nodesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", nodesFile, err)
	}
	var doc nodesDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", nodesFile, err)
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w (found %d, supported %d)", ErrSchemaFuture, doc.SchemaVersion, SchemaVersion)
	}
	if doc.Nodes == nil {
		doc.Nodes = []domain.Node{}
	}
	return doc.Nodes, nil
}

// GetNode returns the node with the given name.
func (s *Store) GetNode(name string) (domain.Node, error) {
	nodes, err := s.LoadNodes()
	if err != nil {
		return domain.Node{}, err
	}
	for _, n := range nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return domain.Node{}, fmt.Errorf("%w: %s", ErrNodeNotFound, name)
}

// AddNode validates and stores a new node. Adding an existing name fails.
func (s *Store) AddNode(n domain.Node) error {
	if err := n.Validate(); err != nil {
		return err
	}
	nodes, err := s.LoadNodes()
	if err != nil {
		return err
	}
	for _, existing := range nodes {
		if existing.Name == n.Name {
			return fmt.Errorf("%w: %s", ErrNodeExists, n.Name)
		}
	}
	return s.saveNodes(append(nodes, n))
}

// UpdateNode replaces the stored record for the node's name.
func (s *Store) UpdateNode(n domain.Node) error {
	if err := n.Validate(); err != nil {
		return err
	}
	nodes, err := s.LoadNodes()
	if err != nil {
		return err
	}
	for i := range nodes {
		if nodes[i].Name == n.Name {
			nodes[i] = n
			return s.saveNodes(nodes)
		}
	}
	return fmt.Errorf("%w: %s", ErrNodeNotFound, n.Name)
}

// ErrNodeHasApps refuses a removal that would orphan application
// records: their release history should not vanish as a side
// effect of removing the node it ran on.
var ErrNodeHasApps = errors.New("node still has mymo applications")

// RemoveNode is the whole of mymo's side of removal: the node's
// record, its dedicated key pair, and nothing else — the box keeps
// everything mymo built there, running and untouched. Refused while
// applications still reference the node; apps are removed one by
// one, each as deliberate as this.
func (s *Store) RemoveNode(name string) error {
	apps, err := s.LoadApps()
	if err != nil {
		return err
	}
	var names []string
	for _, a := range apps {
		if a.Node == name {
			names = append(names, a.Name)
		}
	}
	if len(names) > 0 {
		return fmt.Errorf("%w (%s) — remove them first", ErrNodeHasApps, strings.Join(names, ", "))
	}
	keyPath := filepath.Join(s.dir, "keys", name+".key")
	for _, p := range []string{keyPath, keyPath + ".pub"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.DeleteNode(name)
}

// DeleteNode removes the node's record from local state. The server itself
// is never touched.
func (s *Store) DeleteNode(name string) error {
	nodes, err := s.LoadNodes()
	if err != nil {
		return err
	}
	kept := make([]domain.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Name != name {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(nodes) {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, name)
	}
	return s.saveNodes(kept)
}

// saveNodes writes the nodes document atomically via writeDoc.
func (s *Store) saveNodes(nodes []domain.Node) error {
	if nodes == nil {
		nodes = []domain.Node{}
	}
	return s.writeDoc(nodesFile, nodesDoc{SchemaVersion: SchemaVersion, Nodes: nodes})
}

// writeDoc encodes doc as pretty JSON and replaces the state file named
// name atomically: temp file in the same directory, fsync, rename.
func (s *Store) writeDoc(name string, doc any) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(s.dir, "."+name+".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("set permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("replace %s: %w", name, err)
	}
	return nil
}

// appsFile is the on-disk name of the applications document.
const appsFile = "apps.json"

// appsDoc is the on-disk format of apps.json.
type appsDoc struct {
	SchemaVersion int          `json:"schema_version"`
	Apps          []domain.App `json:"apps"`
}

// LoadApps returns all managed applications. A missing file is an
// empty list.
func (s *Store) LoadApps() ([]domain.App, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, appsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", appsFile, err)
	}
	var doc appsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", appsFile, err)
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w (found %d, supported %d)", ErrSchemaFuture, doc.SchemaVersion, SchemaVersion)
	}
	if doc.Apps == nil {
		doc.Apps = []domain.App{}
	}
	return doc.Apps, nil
}

// GetApp returns the application with the given name.
func (s *Store) GetApp(name string) (domain.App, error) {
	apps, err := s.LoadApps()
	if err != nil {
		return domain.App{}, err
	}
	for _, a := range apps {
		if a.Name == name {
			return a, nil
		}
	}
	return domain.App{}, fmt.Errorf("%w: %s", ErrAppNotFound, name)
}

// AddApp validates and stores a new application. An existing name on
// the same node is the same application and fails; the same name on
// a different node is a different application and fails too — app
// names are one word so they can be typed.
func (s *Store) AddApp(a domain.App) error {
	if err := a.Validate(); err != nil {
		return err
	}
	apps, err := s.LoadApps()
	if err != nil {
		return err
	}
	for _, existing := range apps {
		if existing.Name == a.Name {
			return fmt.Errorf("%w: %s", ErrAppExists, a.Name)
		}
	}
	return s.saveApps(append(apps, a))
}

// UpdateApp replaces the stored record for the app's name.
func (s *Store) UpdateApp(a domain.App) error {
	if err := a.Validate(); err != nil {
		return err
	}
	apps, err := s.LoadApps()
	if err != nil {
		return err
	}
	for i := range apps {
		if apps[i].Name == a.Name {
			apps[i] = a
			return s.saveApps(apps)
		}
	}
	return fmt.Errorf("%w: %s", ErrAppNotFound, a.Name)
}

// DeleteApp removes the application's record from local state. The
// running containers are the engine's to stop, never the store's.
func (s *Store) DeleteApp(name string) error {
	apps, err := s.LoadApps()
	if err != nil {
		return err
	}
	kept := make([]domain.App, 0, len(apps))
	for _, a := range apps {
		if a.Name != name {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(apps) {
		return fmt.Errorf("%w: %s", ErrAppNotFound, name)
	}
	return s.saveApps(kept)
}

// saveApps writes the apps document atomically via writeDoc.
func (s *Store) saveApps(apps []domain.App) error {
	if apps == nil {
		apps = []domain.App{}
	}
	return s.writeDoc(appsFile, appsDoc{SchemaVersion: SchemaVersion, Apps: apps})
}
