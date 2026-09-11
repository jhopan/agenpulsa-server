// Package db: SQLite storage untuk katalog, order, jadwal, setting.
package db

import (
	"database/sql"
	_ "embed"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // driver sqlite (dulu cuma di web_test.go — production lupa)
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite pragmas: WAL biar read ringan, busy_timeout biar tidak tabrak.
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := d.Exec(p); err != nil {
			return nil, err
		}
	}
	if _, err := d.Exec(schemaSQL); err != nil {
		return nil, err
	}
	// Migrasi ringan DB lama: kolom invoice_id (paypan).
	_, _ = d.Exec("ALTER TABLE orders ADD COLUMN invoice_id TEXT")
	return &Store{DB: d}, nil
}

func (s *Store) Close() error { return s.DB.Close() }
