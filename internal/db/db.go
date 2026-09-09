// Package db: SQLite storage untuk katalog, order, jadwal, setting.
package db

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
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
	return &Store{DB: d}, nil
}

func (s *Store) Close() error { return s.DB.Close() }
