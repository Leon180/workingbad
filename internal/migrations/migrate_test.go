package migrations

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestApply_AllTablesAndIndexesPresent is the geological spike check:
// modernc.org/sqlite must support partial unique indexes + FTS5 out of
// the box. Failure here means the locked tech stack assumption is wrong.
func TestApply_AllTablesAndIndexesPresent(t *testing.T) {
	db := openTempDB(t)

	if err := Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	wantTables := []string{
		"entries", "edges",
		"segments", "segment_raw",
		"raw_commits", "raw_changes",
		"sync_state", "source_checkpoint",
		"entries_fts",
	}
	for _, name := range wantTables {
		var found int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ? AND type IN ('table','virtual table')`,
			name,
		).Scan(&found)
		if err != nil {
			t.Fatalf("query for %s: %v", name, err)
		}
		// FTS5 virtual tables register as 'table' in sqlite_master, but the
		// SQL string contains "VIRTUAL TABLE". A COUNT here just confirms
		// presence either way.
		if found != 1 {
			t.Errorf("table %q: count = %d, want 1", name, found)
		}
	}

	// Spot-check the partial unique index on edges — the most fragile DDL.
	var partialEdges int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_edges_live_triple' AND sql LIKE '%WHERE is_current = 1%'`,
	).Scan(&partialEdges)
	if err != nil {
		t.Fatalf("query partial edges index: %v", err)
	}
	if partialEdges != 1 {
		t.Errorf("idx_edges_live_triple partial: count = %d, want 1", partialEdges)
	}
}

// TestApply_Idempotent confirms running Apply twice (e.g. startup auto-migrate
// on every launch) is a no-op the second time. Hard requirement for the
// at-least-once ingest model.
func TestApply_Idempotent(t *testing.T) {
	db := openTempDB(t)

	if err := Apply(db); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	v1, err := Version(db)
	if err != nil {
		t.Fatalf("Version after first: %v", err)
	}

	if err := Apply(db); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	v2, err := Version(db)
	if err != nil {
		t.Fatalf("Version after second: %v", err)
	}
	if v1 != v2 {
		t.Errorf("version changed between idempotent applies: %d -> %d", v1, v2)
	}
}

// TestApply_FTS5_MatchWorks is the headline spike: prove that modernc's
// SQLite has FTS5 actually wired in and that MATCH returns the expected row.
// If this fails, the entire search story falls apart and the tech stack
// needs reconsideration (cgo mattn).
func TestApply_FTS5_MatchWorks(t *testing.T) {
	db := openTempDB(t)
	if err := Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	_, err := db.Exec(
		`INSERT INTO entries_fts (entry_id, title, body) VALUES (?, ?, ?), (?, ?, ?)`,
		"e1", "hello world", "the quick brown fox jumps",
		"e2", "lorem ipsum", "dolor sit amet consectetur",
	)
	if err != nil {
		t.Fatalf("insert into fts: %v", err)
	}

	rows, err := db.Query(`SELECT entry_id FROM entries_fts WHERE entries_fts MATCH ?`, "brown")
	if err != nil {
		t.Fatalf("fts MATCH query: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != 1 || got[0] != "e1" {
		t.Errorf("FTS MATCH 'brown' returned %v, want [e1]", got)
	}
}

// TestApply_PartialUnique_Edges proves that the WHERE is_current = 1 clause
// is actually enforced: two live edges with the same triple should fail.
func TestApply_PartialUnique_Edges(t *testing.T) {
	db := openTempDB(t)
	if err := Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	insert := func(id string, isCurrent int) error {
		_, err := db.Exec(
			`INSERT INTO edges (id, from_id, to_id, relation, is_current, created_at) VALUES (?, 'A', 'B', 'part_of', ?, datetime('now'))`,
			id, isCurrent,
		)
		return err
	}

	if err := insert("edge1", 1); err != nil {
		t.Fatalf("first live insert: %v", err)
	}
	// Second live insert with the same triple must violate the partial unique.
	if err := insert("edge2", 1); err == nil {
		t.Error("expected unique violation on second live (A,B,part_of), got nil")
	}
	// But a superseded (is_current=0) row with the same triple is allowed.
	if err := insert("edge3", 0); err != nil {
		t.Errorf("insert is_current=0 should be allowed: %v", err)
	}
}

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
