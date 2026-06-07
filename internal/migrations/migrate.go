package migrations

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Apply runs all embedded migrations forward against db. Forward-only;
// any failure is fatal per ROADMAP migration discipline ("失敗 fatal abort
// 不降級"). Goose applies each file in its own transaction by default; a
// failed file rolls back leaving the prior state intact.
func Apply(db *sql.DB) error {
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("migrations: set dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("migrations: up: %w", err)
	}
	return nil
}

// Version returns the highest applied migration version, or 0 if none.
// Useful for diagnostics; the CI gates also read this.
func Version(db *sql.DB) (int64, error) {
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return 0, err
	}
	v, err := goose.GetDBVersion(db)
	if err != nil {
		return 0, fmt.Errorf("migrations: version: %w", err)
	}
	return v, nil
}
