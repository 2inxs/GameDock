package rooms

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Комнаты с пробелами", "rooms.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestStoreProtectedRestartUpdateDelete(t *testing.T) {
	store, path := newTestStore(t)
	config, err := store.Save(Config{Name: "Комната друзей", ServerID: "local-server", BootstrapPeers: []string{"tcp://localhost:11010"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ID) != 32 || len(config.Secret) < 40 || config.NetworkName == "" || config.CreatedAt.IsZero() || config.AllowRelay || config.GamePort != 25565 || config.LocalPort != 25566 {
		t.Fatal("room defaults are invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(config.Secret)) {
		t.Fatal("room secret leaked into outer JSON")
	}
	var envelope storeEnvelope
	if err = json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && bytes.Contains(envelope.Data, []byte(config.Secret)) {
		t.Fatal("Windows payload was stored without DPAPI encryption")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("fallback must use owner-only file permissions")
		}
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.List()
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], config) {
		t.Fatal("protected room did not survive restart")
	}
	list[0].BootstrapPeers[0] = "tcp://changed:11111"
	again, _ := reopened.List()
	if again[0].BootstrapPeers[0] != config.BootstrapPeers[0] {
		t.Fatal("List exposed mutable internal room configuration")
	}
	update := config
	update.Name, update.Secret, update.NetworkName = "Новое имя", "", ""
	updated, err := reopened.Save(update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Secret != config.Secret || updated.NetworkName != config.NetworkName || !updated.CreatedAt.Equal(config.CreatedAt) {
		t.Fatal("metadata edit changed room membership or creation time")
	}
	if err = reopened.Delete(config.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err = reopened.List()
	if err != nil || len(list) != 0 {
		t.Fatal("deleted room returned after restart")
	}
	if err = reopened.Delete(config.ID); err == nil {
		t.Fatal("deleting an unknown room succeeded")
	}
}

func TestInvalidSaveAndFailedReplacePreserveMemoryAndFile(t *testing.T) {
	store, path := newTestStore(t)
	config, err := store.Save(Config{Name: "Исходная комната"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := config
	bad.HostVirtualIP = "8.8.8.8"
	if _, err = store.Save(bad); err == nil {
		t.Fatal("saved a public host IP")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid save changed persisted rooms")
	}
	// Replacing an existing directory with a file fails on supported platforms.
	// The original settings file stays untouched while memory must also roll back.
	blocked := filepath.Join(filepath.Dir(path), "blocked")
	if err = os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	store.path = blocked
	bad = config
	bad.Name = "Изменение, которое не должно сохраниться"
	if _, err = store.Save(bad); err == nil {
		t.Fatal("save unexpectedly replaced a directory")
	}
	if err = store.Delete(config.ID); err == nil {
		t.Fatal("delete succeeded despite a failed settings replacement")
	}
	list, _ := store.List()
	if len(list) != 1 || list[0].Name != config.Name {
		t.Fatal("failed write changed in-memory rooms")
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed the original settings file")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".rooms-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("failed write leaked temporary room files")
	}
}

func TestMalformedAndTamperedStoreNeverResets(t *testing.T) {
	store, path := newTestStore(t)
	if _, err := store.Save(Config{Name: "Проверка защиты"}); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope storeEnvelope
	if err = json.Unmarshal(valid, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Data[len(envelope.Data)/2] ^= 0x80
	tampered, _ := json.Marshal(envelope)
	cases := [][]byte{[]byte("{"), []byte("null"), []byte(`{"version":2,"protection":"windows-dpapi-v1","data":""}`), []byte(`{"version":1,"version":1}`), tampered}
	for index, data := range cases {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Fatalf("accepted corrupt room store case %d", index)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(unchanged, data) {
			t.Fatalf("corrupt room store case %d was silently reset", index)
		}
	}
}

func TestConcurrentSavesKeepEveryRoomAndUniqueCredentials(t *testing.T) {
	store, _ := newTestStore(t)
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Save(Config{Name: "Друзья"}); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List()
	if err != nil || len(list) != 12 {
		t.Fatal("concurrent save lost a room")
	}
	ids, secrets, networks := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for _, room := range list {
		if ids[room.ID] || secrets[room.Secret] || networks[room.NetworkName] {
			t.Fatal("room credentials were reused")
		}
		ids[room.ID], secrets[room.Secret], networks[room.NetworkName] = true, true, true
	}
}
