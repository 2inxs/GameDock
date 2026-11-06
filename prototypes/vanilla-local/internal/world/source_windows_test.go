package world

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportRefusesOpenSessionLock(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	fixture(t, source, "original")
	file, err := os.Create(filepath.Join(source, "world", "session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := store.Import(source); err == nil {
		t.Fatal("import accepted an already-open source session")
	}
	assertMissing(t, store.root, "active")
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Import(source); err != nil {
		t.Fatal(err)
	}
}
