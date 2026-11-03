package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxStoreBytes = 4 * 1024 * 1024
const maxStoredRooms = 256

type storeEnvelope struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	Data       []byte `json:"data"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	configs []Config
}

// Open never silently discards unreadable or malformed room data.
func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("укажите абсолютный путь к файлу комнат")
	}
	s := &Store{path: filepath.Clean(path), configs: make([]Config, 0)}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть комнаты: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать комнаты: %w", err)
	}
	if len(data) > maxStoreBytes {
		return nil, errors.New("файл комнат превышает допустимый размер")
	}
	var envelope storeEnvelope
	if err := decodeRoomJSON(data, &envelope); err != nil {
		return nil, errors.New("файл комнат повреждён; исходный файл сохранён без изменений")
	}
	if envelope.Version != 1 || envelope.Protection != protectionName {
		return nil, errors.New("версия или способ защиты файла комнат не поддерживается на этом компьютере")
	}
	plaintext, err := unprotectRooms(envelope.Data)
	if err != nil {
		return nil, errors.New("не удалось расшифровать комнаты для текущего пользователя; исходный файл сохранён без изменений")
	}
	defer clear(plaintext)
	if err = decodeRoomJSON(plaintext, &s.configs); err != nil || s.configs == nil || len(s.configs) > maxStoredRooms {
		return nil, errors.New("неверные данные комнат; исходный файл сохранён без изменений")
	}
	seen := make(map[string]bool)
	for i := range s.configs {
		c := &s.configs[i]
		if err = Validate(*c); err != nil || seen[c.ID] {
			return nil, errors.New("файл содержит неверную или повторяющуюся комнату; исходный файл сохранён без изменений")
		}
		seen[c.ID] = true
		*c = cloneConfig(*c)
	}
	return s, nil
}

func (s *Store) List() ([]Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConfigs(s.configs), nil
}

func (s *Store) Save(config Config) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.configs {
		if s.configs[i].ID == config.ID {
			index = i
			break
		}
	}
	var previous *Config
	if index >= 0 {
		previous = &s.configs[index]
	} else if len(s.configs) >= maxStoredRooms {
		return Config{}, errors.New("достигнут лимит 256 сохранённых комнат")
	}
	prepared, err := prepareConfig(config, previous)
	if err != nil {
		return Config{}, err
	}
	next := cloneConfigs(s.configs)
	if index >= 0 {
		next[index] = prepared
	} else {
		next = append(next, prepared)
	}
	if err = s.persist(next); err != nil {
		return Config{}, err
	}
	s.configs = next
	return cloneConfig(prepared), nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.configs {
		if s.configs[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("комната не найдена")
	}
	next := cloneConfigs(s.configs)
	next = append(next[:index], next[index+1:]...)
	if err := s.persist(next); err != nil {
		return err
	}
	s.configs = next
	return nil
}

func cloneConfigs(configs []Config) []Config {
	result := make([]Config, len(configs))
	for i := range configs {
		result[i] = cloneConfig(configs[i])
	}
	return result
}

func (s *Store) persist(configs []Config) error {
	plaintext, err := json.Marshal(configs)
	if err != nil {
		return errors.New("не удалось подготовить комнаты к сохранению")
	}
	defer clear(plaintext)
	protected, err := protectRooms(plaintext)
	if err != nil {
		return errors.New("не удалось защитить ключи комнат для текущего пользователя")
	}
	data, err := json.MarshalIndent(storeEnvelope{Version: 1, Protection: protectionName, Data: protected}, "", "  ")
	if err != nil || len(data) > maxStoreBytes {
		return errors.New("не удалось сохранить комнаты: превышен допустимый размер")
	}
	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("не удалось создать папку настроек комнат: %w", err)
	}
	f, err := os.CreateTemp(dir, ".rooms-*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось подготовить файл комнат: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("не удалось записать комнаты: %w", err)
	}
	if err = replaceRoomFile(tmp, s.path); err != nil {
		return fmt.Errorf("не удалось заменить файл комнат; прежние настройки сохранены: %w", err)
	}
	return nil
}
