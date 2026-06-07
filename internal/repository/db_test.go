package repository

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestOpen_AppliesMigrations ensures Open runs migrations end-to-end against a
// fresh file. The migrations package owns the schema assertions; here we
// just verify the wire-up.
func TestOpen_AppliesMigrations(t *testing.T) {
	db := openTempDB(t)

	// Sanity: one of the locked tables is present.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'entries'`,
	).Scan(&n); err != nil {
		t.Fatalf("query entries: %v", err)
	}
	if n != 1 {
		t.Errorf("entries table missing after Open")
	}
}

// TestOpen_PragmasApplied confirms the four PRAGMAs took effect on the
// connection pool. Failures here either mean modernc doesn't honor the
// statement or the pool started handing out fresh connections (pool=1 should
// prevent that).
func TestOpen_PragmasApplied(t *testing.T) {
	db := openTempDB(t)

	type pragma struct {
		name, want string
	}
	checks := []pragma{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // NORMAL = 1
	}
	for _, c := range checks {
		var got string
		if err := db.QueryRow("PRAGMA " + c.name).Scan(&got); err != nil {
			t.Fatalf("query PRAGMA %s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("PRAGMA %s = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestOpen_IdempotentReopen — close then reopen the same file. Migrations
// must be no-op the second time (startup auto-migrate model).
func TestOpen_IdempotentReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (reopen): %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'entries_fts'`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Error("entries_fts disappeared between opens")
	}
}

// TestOpen_DataPersists writes a row, closes, reopens, and reads it back.
// Smoke test for the embedded driver + WAL.
func TestOpen_DataPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db1.Exec(
		`INSERT INTO raw_changes (change_id, repo_id, patch_id, created_at) VALUES (?, ?, ?, datetime('now'))`,
		"c1", "r1", "pid-1",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	var patchID string
	if err := db2.QueryRow(
		`SELECT patch_id FROM raw_changes WHERE change_id = ?`, "c1",
	).Scan(&patchID); err != nil {
		t.Fatalf("read: %v", err)
	}
	if patchID != "pid-1" {
		t.Errorf("patch_id = %q, want pid-1", patchID)
	}
}

// TestOpen_FK_OnDeletes_Block — foreign_keys=ON should prevent inserting a
// raw_commit referencing a nonexistent change. Belt-and-suspenders for the
// PRAGMA assertion above.
func TestOpen_FK_Enforced(t *testing.T) {
	db := openTempDB(t)

	_, err := db.Exec(
		`INSERT INTO raw_commits (sha, repo_id, change_id, author, author_time, committer, commit_time, message, created_at)
		 VALUES ('sha-1','r1','c-missing','a','t','c','t','m', datetime('now'))`,
	)
	if err == nil {
		t.Error("expected FK violation; got nil — foreign_keys not enforced")
	}
}

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
