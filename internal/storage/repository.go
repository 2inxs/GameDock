package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gamedock/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("сервер не найден")

// Repository stores configuration only. It never manages server directories.
type Repository struct{ db *sql.DB }

func OpenDefault() (*Repository, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("не удалось найти папку настроек: %w", err)
	}
	return Open(filepath.Join(dir, "GameDock", "gamedock.db"))
}

func Open(path string) (*Repository, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("не удалось создать папку базы данных: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть базу данных: %w", err)
	}
	// One connection also keeps an in-memory repository consistent.
	db.SetMaxOpenConns(1)
	r := &Repository{db: db}
	if _, err = db.Exec("PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;"); err == nil {
		err = r.migrate(context.Background())
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("не удалось подготовить базу данных: %w", err)
	}
	return r, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) migrate(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return fmt.Errorf("версия базы данных %d новее поддерживаемой", version)
	}
	if version == 0 {
		_, err = tx.ExecContext(ctx, `
CREATE TABLE servers (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, game TEXT NOT NULL,
 java_path TEXT NOT NULL, jar_path TEXT NOT NULL, files_dir TEXT NOT NULL,
 working_dir TEXT NOT NULL, min_memory_mb INTEGER NOT NULL,
 max_memory_mb INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK (id = 1), library_dir TEXT NOT NULL);
INSERT INTO settings (id, library_dir) VALUES (1, '');
PRAGMA user_version = 1;`)
		if err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE servers ADD COLUMN java_args_file TEXT NOT NULL DEFAULT ''; PRAGMA user_version = 2;`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const serverColumns = "id, name, game, java_path, jar_path, files_dir, working_dir, min_memory_mb, max_memory_mb, created_at, updated_at, java_args_file"

type scanner interface{ Scan(...any) error }

func scanServer(row scanner) (model.Server, error) {
	var s model.Server
	var created, updated string
	err := row.Scan(&s.ID, &s.Name, &s.Game, &s.JavaPath, &s.JarPath, &s.FilesDir, &s.WorkingDir, &s.MinMemoryMB, &s.MaxMemoryMB, &created, &updated, &s.JavaArgsFile)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return s, err
	}
	s.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return s, err
}

func (r *Repository) ListServers(ctx context.Context) ([]model.Server, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+serverColumns+" FROM servers ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Server, 0)
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *Repository) GetServer(ctx context.Context, id string) (model.Server, error) {
	return scanServer(r.db.QueryRowContext(ctx, "SELECT "+serverColumns+" FROM servers WHERE id = ?", id))
}

func (r *Repository) SaveServer(ctx context.Context, s model.Server) (model.Server, error) {
	now := time.Now().UTC()
	if s.ID == "" {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return s, err
		}
		s.ID = hex.EncodeToString(bytes[:])
		s.CreatedAt = now
	} else {
		previous, err := r.GetServer(ctx, s.ID)
		if err != nil {
			return s, err
		}
		s.CreatedAt = previous.CreatedAt
	}
	s.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `INSERT INTO servers (`+serverColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, game=excluded.game, java_path=excluded.java_path,
jar_path=excluded.jar_path, files_dir=excluded.files_dir, working_dir=excluded.working_dir,
min_memory_mb=excluded.min_memory_mb, max_memory_mb=excluded.max_memory_mb, updated_at=excluded.updated_at,
java_args_file=excluded.java_args_file`,
		s.ID, s.Name, s.Game, s.JavaPath, s.JarPath, s.FilesDir, s.WorkingDir, s.MinMemoryMB, s.MaxMemoryMB, s.CreatedAt.Format(time.RFC3339Nano), s.UpdatedAt.Format(time.RFC3339Nano), s.JavaArgsFile)
	return s, err
}

func (r *Repository) DeleteServer(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM servers WHERE id = ?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}

func (r *Repository) GetSettings(ctx context.Context) (model.Settings, error) {
	var settings model.Settings
	err := r.db.QueryRowContext(ctx, "SELECT library_dir FROM settings WHERE id = 1").Scan(&settings.LibraryDir)
	return settings, err
}

func (r *Repository) SaveSettings(ctx context.Context, settings model.Settings) (model.Settings, error) {
	_, err := r.db.ExecContext(ctx, "UPDATE settings SET library_dir = ? WHERE id = 1", settings.LibraryDir)
	return settings, err
}
