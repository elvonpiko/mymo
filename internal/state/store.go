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

// saveNodes writes the nodes document atomically: a temp file in the same
// directory, fsync, then rename over the target.
func (s *Store) saveNodes(nodes []domain.Node) error {
	if nodes == nil {
		nodes = []domain.Node{}
	}
	doc := nodesDoc{SchemaVersion: SchemaVersion, Nodes: nodes}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", nodesFile, err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(s.dir, "."+nodesFile+".tmp-")
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
	if err := os.Rename(tmpName, filepath.Join(s.dir, nodesFile)); err != nil {
		return fmt.Errorf("replace %s: %w", nodesFile, err)
	}
	return nil
}
