// Package db: SQLite storage untuk katalog, order, jadwal, setting.
package db

import (
	"database/sql"
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // driver sqlite (dulu cuma di web_test.go — production lupa)
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	DB   *sql.DB
	path string
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
	// SQLite pragmas: WAL biar read ringan, busy_timeout biar tidak tabrak,
	// wal_autocheckpoint + synchronous=NORMAL biar WAL tidak membengkak
	// (dulu WAL 2.4MB untuk DB 4KB karena tidak ada checkpoint).
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA wal_autocheckpoint=1000",  // checkpoint tiap ~1000 halaman WAL
		"PRAGMA synchronous=NORMAL",      // WAL: fsync per checkpoint, bukan per transaksi
	} {
		if _, err := d.Exec(p); err != nil {
			return nil, err
		}
	}
	if _, err := d.Exec(schemaSQL); err != nil {
		return nil, err
	}
	// Migrasi ringan DB lama: kolom invoice_id (paypan), operator (guard provider).
	_, _ = d.Exec("ALTER TABLE orders ADD COLUMN invoice_id TEXT")
	_, _ = d.Exec("ALTER TABLE catalog ADD COLUMN operator TEXT")
	// expires_at: deadline bayar invoice (unix) — biar order pending bisa
	// di-expire lokal 5 menit tanpa nunggu Paypan.
	_, _ = d.Exec("ALTER TABLE orders ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0")
	// client_ip: lokasi client untuk limit order aktif (3 per lokasi).
	_, _ = d.Exec("ALTER TABLE orders ADD COLUMN client_ip TEXT")
	return &Store{DB: d, path: path}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Checkpoint: paksa WAL di-flush ke file DB utama + vacuumoccasionally.
// Dipanggil loop berkala supaya WAL tidak tumbuh tak terbatas.
func (s *Store) Checkpoint() error {
	_, err := s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// DBSize: ukuran file DB + WAL (byte) — buat monitoring.
func (s *Store) DBSize() (db int64, wal int64) {
	if fi, err := os.Stat(s.path); err == nil {
		db = fi.Size()
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil {
		wal = fi.Size()
	}
	return
}

// CheckpointLoop: flush WAL ke file DB utama tiap 5 menit + vacuum ringan.
// Tanpa ini WAL bisa tumbuh besar (dulu 2.4MB untuk DB 4KB) dan write
// makin lambat.
func (s *Store) CheckpointLoop() {
	for {
		time.Sleep(5 * time.Minute)
		if err := s.Checkpoint(); err != nil {
			log.Printf("[DB] checkpoint: %v", err)
		}
	}
}
