package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMetaDefaultsOnFirstRun(t *testing.T) {
	s := openTestStore(t)
	meta, err := s.LoadMeta()
	if err != nil {
		t.Fatalf("LoadMeta() = %v, want nil", err)
	}
	if meta.IntroSeen {
		t.Fatal("first run: IntroSeen = true, want false")
	}
}

func TestSaveMetaRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveMeta(MetaDoc{IntroSeen: true}); err != nil {
		t.Fatalf("SaveMeta() = %v, want nil", err)
	}
	meta, err := s.LoadMeta()
	if err != nil {
		t.Fatalf("LoadMeta() = %v, want nil", err)
	}
	if !meta.IntroSeen {
		t.Fatal("IntroSeen = false, want true after save")
	}
	if meta.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", meta.SchemaVersion, SchemaVersion)
	}
}

func TestSaveMetaFilePermissions(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveMeta(MetaDoc{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir(), metaFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != filePerm {
		t.Fatalf("file perms = %o, want %o", perm, filePerm)
	}
}

func TestLoadMetaRejectsFutureSchema(t *testing.T) {
	s := openTestStore(t)
	b := []byte(`{"schema_version": 2}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), metaFile), b, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadMeta(); !errors.Is(err, ErrSchemaFuture) {
		t.Fatalf("LoadMeta() = %v, want ErrSchemaFuture", err)
	}
}

func TestLoadMetaRejectsCorruptFile(t *testing.T) {
	s := openTestStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), metaFile), []byte("{"), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadMeta(); err == nil {
		t.Fatal("LoadMeta() = nil, want parse error")
	}
}
