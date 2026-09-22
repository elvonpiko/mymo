package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// metaFile stores small ui/runtime flags (like the first-run intro marker).
const metaFile = "meta.json"

// MetaDoc is the on-disk format of meta.json.
type MetaDoc struct {
	SchemaVersion int  `json:"schema_version"`
	IntroSeen     bool `json:"intro_seen"`
}

// LoadMeta returns the meta document. A missing file means defaults,
// i.e. a first run.
func (s *Store) LoadMeta() (MetaDoc, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return MetaDoc{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return MetaDoc{}, fmt.Errorf("read %s: %w", metaFile, err)
	}
	var doc MetaDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return MetaDoc{}, fmt.Errorf("parse %s: %w", metaFile, err)
	}
	if doc.SchemaVersion > SchemaVersion {
		return MetaDoc{}, fmt.Errorf("%w (found %d, supported %d)", ErrSchemaFuture, doc.SchemaVersion, SchemaVersion)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	return doc, nil
}

// SaveMeta writes the meta document atomically with the current schema.
func (s *Store) SaveMeta(m MetaDoc) error {
	m.SchemaVersion = SchemaVersion
	return s.writeDoc(metaFile, m)
}
