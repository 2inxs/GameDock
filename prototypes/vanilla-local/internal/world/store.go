// Package world manages the stopped server's local files and snapshots.
package world

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gamedock/internal/vanilla"
)

const backupLayout = "20060102T150405.000000000Z"

var configFiles = []string{
	"ops.json", "whitelist.json", "banned-ips.json", "banned-players.json",
}

// Store owns a server directory and its backups. Callers serialize access and
// stop the server before making a backup or changing the active directory.
type Store struct {
	root string
}

// Open creates the store and recovers interrupted filesystem operations.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("не указана папка данных GameDock")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if err := initializeRoot(root); err != nil {
		return nil, err
	}
	store := &Store{root: root}
	if err := os.Mkdir(filepath.Join(root, "backups"), 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	if err := store.Recover(); err != nil {
		return nil, err
	}
	return store, nil
}

const rootFormat = "GameDock prototype storage v1\n"

func initializeRoot(root string) error {
	marker := filepath.Join(root, ".gamedock-store")
	info, err := os.Lstat(marker)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() != int64(len(rootFormat)) {
			return errors.New("неверный маркер хранилища GameDock")
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if string(data) != rootFormat {
			return errors.New("неизвестный формат хранилища GameDock")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".gamedock.lock" || !entry.Type().IsRegular() {
			return errors.New("для нового хранилища GameDock нужна пустая папка")
		}
	}
	return os.WriteFile(marker, []byte(rootFormat), 0600)
}

func (s *Store) ActiveDir() string { return filepath.Join(s.root, "active") }

// HasActive reports whether a managed server directory exists.
func (s *Store) HasActive() (bool, error) { return directoryExists(s.ActiveDir()) }

// Import copies supported data from a separate, stopped Vanilla server.
func (s *Store) Import(source string) error {
	if source == "" {
		return errors.New("не указана папка исходного сервера")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if present, err := directoryExists(source); err != nil {
		return err
	} else if !present {
		return fmt.Errorf("папка исходного сервера отсутствует: %s", source)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if contains(source, s.root) || contains(s.root, source) {
		return errors.New("папка исходного сервера должна находиться отдельно от папки данных GameDock")
	}
	for _, name := range []string{"mods", "plugins", "world_nether", "world_the_end"} {
		if _, err := os.Lstat(filepath.Join(source, name)); err == nil {
			return fmt.Errorf("папка %s указывает на неподдерживаемую сборку. Выберите Vanilla", name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := requireDirectory(filepath.Join(source, "world")); err != nil {
		return err
	}
	unlock, err := lockSourceWorld(filepath.Join(source, "world", "session.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return s.replace(source)
}

// Backup publishes a snapshot only after the entire copy has succeeded.
func (s *Store) Backup() (string, error) {
	if err := s.Recover(); err != nil {
		return "", err
	}
	backups := filepath.Join(s.root, "backups")
	pending := filepath.Join(backups, ".pending")
	if err := os.Mkdir(pending, 0700); err != nil {
		return "", err
	}
	defer os.RemoveAll(pending)
	if err := copyData(s.ActiveDir(), pending); err != nil {
		return "", err
	}
	for stamp := time.Now().UTC(); ; stamp = stamp.Add(time.Nanosecond) {
		id := stamp.Format(backupLayout)
		destination := filepath.Join(backups, id)
		if _, err := os.Lstat(destination); errors.Is(err, fs.ErrNotExist) {
			if err := os.Rename(pending, destination); err != nil {
				return "", err
			}
			return id, nil
		} else if err != nil {
			return "", err
		}
	}
}

func (s *Store) ListBackups() ([]string, error) {
	backups := filepath.Join(s.root, "backups")
	if err := requireDirectory(backups); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validBackupID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

func (s *Store) Restore(backupID string) error {
	if !validBackupID(backupID) {
		return errors.New("недопустимый идентификатор резервной копии")
	}
	return s.replace(filepath.Join(s.root, "backups", backupID))
}

func validBackupID(id string) bool {
	stamp, err := time.Parse(backupLayout, id)
	return err == nil && stamp.Format(backupLayout) == id
}

// Recover rolls back a replacement interrupted before its commit rename, or
// archives the previous copy after a successful commit. The directory layout is
// the transaction record: active absent + previous present means roll back.
func (s *Store) Recover() error {
	active, err := s.HasActive()
	if err != nil {
		return err
	}
	previous := filepath.Join(s.root, ".previous")
	hasPrevious, err := directoryExists(previous)
	if err != nil {
		return err
	}
	if hasPrevious && !active {
		if err := os.Rename(previous, s.ActiveDir()); err != nil {
			return fmt.Errorf("не удалось восстановить предыдущий сервер: %w", err)
		}
	}
	if hasPrevious && active {
		if err := s.archivePrevious(previous); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(s.root, ".incoming")); err != nil {
		return err
	}
	backups := filepath.Join(s.root, "backups")
	if err := requireDirectory(backups); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(backups, ".pending"))
}

func (s *Store) replace(source string) error {
	if err := s.Recover(); err != nil {
		return err
	}
	active := s.ActiveDir()
	incoming := filepath.Join(s.root, ".incoming")
	previous := filepath.Join(s.root, ".previous")
	if err := os.Mkdir(incoming, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(incoming)
	if err := copyData(source, incoming); err != nil {
		return err
	}
	hasActive, err := s.HasActive()
	if err != nil {
		return err
	}
	if hasActive {
		for _, name := range []string{"server.jar", "eula.txt"} {
			if err := copyOptional(active, incoming, name); err != nil {
				return err
			}
		}
		if err := os.Rename(active, previous); err != nil {
			return err
		}
	}
	if err := os.Rename(incoming, active); err != nil {
		return errors.Join(err, s.Recover())
	}
	return s.Recover()
}

func (s *Store) archivePrevious(previous string) error {
	recovery := filepath.Join(s.root, "recovery")
	if err := os.Mkdir(recovery, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := requireDirectory(recovery); err != nil {
		return err
	}
	for stamp := time.Now().UTC(); ; stamp = stamp.Add(time.Nanosecond) {
		target := filepath.Join(recovery, stamp.Format(backupLayout))
		_, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return os.Rename(previous, target)
		}
		if err != nil {
			return err
		}
	}
}

func contains(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func directoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return false, fmt.Errorf("ожидалась обычная папка: %s", path)
	}
	return true, nil
}

func requireDirectory(path string) error {
	present, err := directoryExists(path)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("папка отсутствует: %s", path)
	}
	return nil
}

func copyData(source, destination string) error {
	if err := requireDirectory(source); err != nil {
		return err
	}
	world := filepath.Join(source, "world")
	if err := requireDirectory(world); err != nil {
		return err
	}
	for _, name := range configFiles {
		if err := copyOptional(source, destination, name); err != nil {
			return err
		}
	}
	properties := filepath.Join(source, "server.properties")
	if _, err := os.Lstat(properties); err == nil {
		temporary := filepath.Join(destination, "server.properties")
		if err := copyRegular(properties, temporary); err != nil {
			return err
		}
		if err := checkLevelName(temporary); err != nil {
			return err
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	sessionLock := filepath.Join(world, "session.lock")
	err := filepath.WalkDir(world, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == sessionLock && !entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("недопустимое имя в папке мира: %s", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := requireDirectory(path); err != nil {
				return err
			}
			return os.Mkdir(target, 0700)
		}
		return copyRegular(path, target)
	})
	if err != nil {
		return err
	}
	return vanilla.ValidateWorld(filepath.Join(destination, "world"))
}

func copyOptional(source, destination, name string) error {
	path := filepath.Join(source, name)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return copyRegular(path, filepath.Join(destination, name))
}

func copyRegular(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ожидался обычный файл: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("файл изменился перед копированием: %s", source)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	return errors.Join(copyErr, output.Close())
}
