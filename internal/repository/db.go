// Package repository owns the SQLite *sql.DB lifecycle and (later) the
// single-writer repository service that is the sole entry into the schema
// (truth-source-schema invariant 10). Phase 0 only sets up Open + migrate;
// the service layer follows in Phase 1 Slice A.
package repository

import (
	"database/sql"
	"fmt"

	"github.com/Leon180/workingbad/internal/migrations"
	_ "modernc.org/sqlite" // database/sql driver registration
)

// Open opens the SQLite database at path, applies recommended PRAGMAs,
// runs forward-only migrations, and returns a ready-to-use *sql.DB.
//
// Pool size is intentionally 1 — matches the "single-writer choke point"
// architectural invariant. When read concurrency becomes a bottleneck we
// will introduce a separate read pool via a connector hook, not by lifting
// this cap (which would invite race-window bugs the model is designed to
// avoid).
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, fmt.Errorf("repository: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := setPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrations.Apply(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repository: migrate: %w", err)
	}
	return db, nil
}

// setPragmas applies the recommended PRAGMAs. journal_mode=WAL is persistent
// across connections (lives in the file); the rest are per-connection but with
// MaxOpenConns(1) they stick.
func setPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("repository: %s: %w", p, err)
		}
	}
	return nil
}
