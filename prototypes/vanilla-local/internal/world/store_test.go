package world

import (
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestImportBackupRestore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	fixture(t, source, "original")
	write(t, source, "server.jar", "untrusted")
	write(t, source, "eula.txt", "eula=true")
	write(t, source, "world/session.lock", "stale lock")
	write(t, source, "ops.json", "[]")
	if err := store.Import(source); err != nil {
		t.Fatal(err)
	}
	active := store.ActiveDir()
	assertFile(t, active, "world/region/r.0.0.mca", "original")
	assertFile(t, active, "ops.json", "[]")
	assertMissing(t, active, "server.jar")
	assertMissing(t, active, "eula.txt")
	assertMissing(t, active, "world/session.lock")

	write(t, active, "server.jar", "trusted")
	write(t, active, "eula.txt", "eula=true")
	id, err := store.Backup()
	if err != nil {
		t.Fatal(err)
	}
	assertMissing(t, filepath.Join(store.root, "backups", id), "server.jar")
	ids, err := store.ListBackups()
	if err != nil || !reflect.DeepEqual(ids, []string{id}) {
		t.Fatalf("unexpected backup list %v, %v", ids, err)
	}
	write(t, active, "world/region/r.0.0.mca", "changed")
	if err := store.Restore(id); err != nil {
		t.Fatal(err)
	}
	assertFile(t, active, "world/region/r.0.0.mca", "original")
	assertFile(t, active, "server.jar", "trusted")
	assertFile(t, active, "eula.txt", "eula=true")
	assertFile(t, source, "server.jar", "untrusted")
	assertMissing(t, filepath.Join(store.root, "backups", id), "server.properties")
	assertRecovery(t, store.root, "changed")
	if err := store.Restore("../../outside"); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestRecoverInterruptedReplacement(t *testing.T) {
	for _, committed := range []bool{false, true} {
		label, want := "before commit", "old"
		if committed {
			label, want = "after commit", "new"
		}
		t.Run(label, func(t *testing.T) {
			root := t.TempDir()
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			fixture(t, filepath.Join(root, ".previous"), "old")
			incoming := filepath.Join(root, ".incoming")
			fixture(t, incoming, "new")
			if committed {
				if err := os.Rename(incoming, store.ActiveDir()); err != nil {
					t.Fatal(err)
				}
			}
			write(t, root, "backups/.pending/partial", "partial backup")
			store, err = Open(root)
			if err != nil {
				t.Fatal(err)
			}
			assertFile(t, store.ActiveDir(), "world/region/r.0.0.mca", want)
			assertMissing(t, root, ".incoming")
			assertMissing(t, root, ".previous")
			assertMissing(t, root, "backups/.pending")
			if committed {
				assertRecovery(t, root, "old")
			}
		})
	}
}

func TestFailedImportPreservesActive(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture(t, store.ActiveDir(), "old")
	source := t.TempDir()
	fixture(t, source, "new")
	for _, properties := range []string{"level-name=../other\n", "motd=hi\rlevel-name=other\r", "\\u006cevel-name=other\n", "online-mode=false\n"} {
		write(t, source, "server.properties", properties)
		if err := store.Import(source); err == nil {
			t.Fatalf("accepted unsupported properties %q", properties)
		}
		assertFile(t, store.ActiveDir(), "world/region/r.0.0.mca", "old")
		assertMissing(t, store.root, ".incoming")
	}
	write(t, source, "server.properties", "level-name=world\n")
	write(t, source, "world/level.dat", "invalid metadata")
	if err := store.Import(source); err == nil {
		t.Fatal("accepted an invalid world")
	}
	assertFile(t, store.ActiveDir(), "world/region/r.0.0.mca", "old")
}

func TestRefuseUnownedDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".incoming/important", "keep")
	write(t, root, "backups/.pending/important", "keep")
	if _, err := Open(root); err == nil {
		t.Fatal("accepted a non-empty directory without GameDock marker")
	}
	assertFile(t, root, ".incoming/important", "keep")
	assertFile(t, root, "backups/.pending/important", "keep")
}

func assertRecovery(t *testing.T, root, want string) {
	t.Helper()
	recovery := filepath.Join(root, "recovery")
	entries, err := os.ReadDir(recovery)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected recovery folders: %v, %v", entries, err)
	}
	assertFile(t, filepath.Join(recovery, entries[0].Name()), "world/region/r.0.0.mca", want)
}

func TestRejectSymlinkInWorld(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	fixture(t, source, "data")
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "world", "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Import(source); err == nil {
		t.Fatal("accepted a symlink inside the world")
	}
	assertMissing(t, store.root, "active")
}

func fixture(t *testing.T, dir, marker string) {
	t.Helper()
	write(t, dir, "server.properties", "level-name=world\nmotd=Fixture\n")
	write(t, dir, "world/region/r.0.0.mca", marker)
	file, err := os.Create(filepath.Join(dir, "world", "level.dat"))
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	metadata := []byte("\x0a\x00\x00\x0a\x00\x04Data\x03\x00\x0bDataVersion")
	metadata = binary.BigEndian.AppendUint32(metadata, 4671)
	metadata = append(metadata, 0, 0)
	_, writeErr := writer.Write(metadata)
	if err := errors.Join(writeErr, writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, dir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v, want %q", name, data, err, want)
	}
}

func assertMissing(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", name, err)
	}
}
