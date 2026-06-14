package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/domain"
)

// defaultSearchLimit caps SearchNodes when the caller passes limit <= 0.
const defaultSearchLimit = 50

// validNodeTypes is the closed enum a node's primary type may take — the
// same set as entries.type. Unlike secondary labels (which exclude goal),
// a node CAN be a goal (it's an aggregation-root identity).
var validNodeTypes = map[domain.EntryType]struct{}{
	domain.EntryTypeActivity: {},
	domain.EntryTypeResearch: {},
	domain.EntryTypeDiscuss:  {},
	domain.EntryTypeDecision: {},
	domain.EntryTypeGoal:     {},
}

// nodeColumns is the SELECT column list scanNodeRows expects, in order.
// Kept in one place so GetNode / NodesForEntry / GetLiveNodeByLogicalID /
// SearchNodes stay in lockstep with the scanner. Every column is qualified
// with the `n` alias so the list is safe to embed in joins where another table
// (e.g. nodes_fts) shares column names like title/body; all callers therefore
// alias the nodes table as `n`. COALESCE bridges D1 rows (added before the D2
// supersede columns existed) — version→1, occurred/ingested→created_at.
const nodeColumns = `n.id, COALESCE(n.logical_id, n.id), n.type, n.title, n.body, COALESCE(n.status, ''),
       COALESCE(n.version, 1), n.is_current, COALESCE(n.superseded_by, ''),
       COALESCE(n.occurred_at, n.ingested_at, n.created_at),
       COALESCE(n.ingested_at, n.created_at),
       n.created_at, n.updated_at`

// validateNode enforces the per-type contract a node must satisfy. Shared by
// CreateNode and SupersedeNode so both reject the same bad inputs.
func validateNode(n domain.Node) error {
	if n.Title == "" {
		return fmt.Errorf("%w: node title required", ErrInvalidInput)
	}
	if _, ok := validNodeTypes[n.Type]; !ok {
		return fmt.Errorf("%w: node type %q not in {activity,research,discuss,decision,goal}", ErrInvalidInput, n.Type)
	}
	if n.Type == domain.EntryTypeGoal && n.Status == "" {
		return fmt.Errorf("%w: goal node requires status", ErrInvalidInput)
	}
	return nil
}

// CreateNode inserts a new live node (version 1, its own logical root). The
// migration backfills nodes from entries; this is the programmatic path the
// split/aggregate pipeline (Slices G/H) writes through. Assigns id/logical_id
// when empty and stamps the four timestamps when zero.
func (s *Service) CreateNode(ctx context.Context, n domain.Node) (domain.Node, error) {
	if err := validateNode(n); err != nil {
		return domain.Node{}, err
	}
	if n.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return domain.Node{}, fmt.Errorf("repository: generate node id: %w", err)
		}
		n.ID = id.String()
	}
	if n.LogicalID == "" {
		n.LogicalID = n.ID // a fresh node is its own logical root
	}
	if n.Version <= 0 {
		n.Version = 1
	}
	n.IsCurrent = true
	n.SupersededBy = ""
	now := time.Now().UTC()
	if n.OccurredAt.IsZero() {
		n.OccurredAt = now
	}
	if n.IngestedAt.IsZero() {
		n.IngestedAt = now
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = now
	}

	// Node row + its FTS mirror commit atomically (entries_fts contract).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes
		   (id, logical_id, type, title, body, status, version, is_current,
		    superseded_by, occurred_at, ingested_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, ?, ?)`,
		n.ID, n.LogicalID, string(n.Type), n.Title, n.Body, string(n.Status), n.Version,
		n.OccurredAt.Format(time.RFC3339Nano), n.IngestedAt.Format(time.RFC3339Nano),
		n.CreatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return domain.Node{}, fmt.Errorf("repository: insert node: %w", err)
	}
	if err := insertNodeFTS(ctx, tx, n); err != nil {
		return domain.Node{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Node{}, fmt.Errorf("repository: commit: %w", err)
	}
	return n, nil
}

// nodeFTSExecer is the subset of *sql.Tx the FTS maintenance helpers need. FTS
// maintenance ALWAYS runs inside the node-write tx so the index commits
// atomically with the node row (own-content, no triggers — entries_fts
// contract, see migration 0007 / 0015).
type nodeFTSExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertNodeFTS mirrors a live node into nodes_fts.
func insertNodeFTS(ctx context.Context, tx nodeFTSExecer, n domain.Node) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes_fts (node_id, title, body) VALUES (?, ?, ?)`,
		n.ID, n.Title, n.Body); err != nil {
		return fmt.Errorf("repository: nodes_fts insert: %w", err)
	}
	return nil
}

// deleteNodeFTS drops a node's nodes_fts row — called when it stops being live
// (supersede), so the index mirrors is_current=1 nodes only.
func deleteNodeFTS(ctx context.Context, tx nodeFTSExecer, nodeID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM nodes_fts WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("repository: nodes_fts delete: %w", err)
	}
	return nil
}

// SupersedeNode appends a new version of a node and retires the old one — the
// node-layer analogue of Supersede(entry) under model A. The replacement
// inherits LogicalID + OccurredAt (the work's event time is stable across
// edits) and gets Version+1 with a fresh id; the old row flips
// is_current=0 / superseded_by=<new id>. All in one tx.
//
// expectedVersion is the optimistic lock: pass the Version observed when the
// caller read the live node; a mismatch returns ErrVersionConflict (a
// concurrent edit won). Pass 0 to skip the check (single-writer paths).
//
// Edges are NOT touched here: they key on entries.logical_id (decision (a),
// migration 0017), not on node.id, so a node supersede never dangles an edge.
// When Slices G/H wire the node layer into edge reads, revisit this.
func (s *Service) SupersedeNode(ctx context.Context, oldID string, expectedVersion int, replacement domain.Node) (domain.Node, error) {
	if oldID == "" {
		return domain.Node{}, fmt.Errorf("%w: node id required", ErrInvalidInput)
	}
	if err := validateNode(replacement); err != nil {
		return domain.Node{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var logicalID, occurred string
	var version int
	var isCurrent int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(logical_id, id), COALESCE(version, 1), is_current,
		        COALESCE(occurred_at, ingested_at, created_at)
		   FROM nodes WHERE id = ?`, oldID).
		Scan(&logicalID, &version, &isCurrent, &occurred)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, fmt.Errorf("%w: node %s", ErrNotFound, oldID)
	}
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: load node %s: %w", oldID, err)
	}
	if isCurrent != 1 {
		return domain.Node{}, fmt.Errorf("%w: node %s is not the live version", ErrNotFound, oldID)
	}
	if expectedVersion > 0 && version != expectedVersion {
		return domain.Node{}, fmt.Errorf("%w: node %s expected v%d, live is v%d", ErrVersionConflict, oldID, expectedVersion, version)
	}

	newID, err := uuid.NewV7()
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: generate node id: %w", err)
	}
	r := replacement
	r.ID = newID.String()
	r.LogicalID = logicalID
	r.Version = version + 1
	r.IsCurrent = true
	r.SupersededBy = ""
	now := time.Now().UTC()
	// occurred_at: keep the work's event time; supersede is an edit, not a
	// new event (mirrors Supersede(entry)).
	if r.OccurredAt.IsZero() {
		if r.OccurredAt, err = parseRFC(occurred); err != nil {
			return domain.Node{}, fmt.Errorf("repository: parse node %s occurred_at: %w", oldID, err)
		}
	}
	r.IngestedAt = now
	r.CreatedAt = now
	r.UpdatedAt = now

	// Retire the old version FIRST, then insert the new live one. The
	// idx_nodes_logical_live UNIQUE(logical_id) WHERE is_current=1 partial
	// index is checked per-statement, so inserting the new live row while the
	// old is still is_current=1 would violate it. Flip-then-insert keeps at
	// most one live row per logical chain at every statement boundary.
	//
	// The WHERE … AND is_current = 1 guard + RowsAffected check makes the
	// optimistic lock explicit in the SQL rather than relying solely on the
	// single-connection serialization (db.go MaxOpenConns(1)): if a concurrent
	// writer already retired this head between our SELECT and here, the UPDATE
	// touches 0 rows and we surface ErrVersionConflict instead of an opaque
	// UNIQUE failure at the subsequent INSERT.
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET is_current = 0, superseded_by = ?, updated_at = ?
		   WHERE id = ? AND is_current = 1`,
		r.ID, now.Format(time.RFC3339Nano), oldID)
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: retire node %s: %w", oldID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return domain.Node{}, fmt.Errorf("repository: retire node %s rows: %w", oldID, err)
	} else if n != 1 {
		return domain.Node{}, fmt.Errorf("%w: node %s was concurrently superseded", ErrVersionConflict, oldID)
	}
	// The retired version is no longer live → drop its FTS mirror.
	if err := deleteNodeFTS(ctx, tx, oldID); err != nil {
		return domain.Node{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes
		   (id, logical_id, type, title, body, status, version, is_current,
		    superseded_by, occurred_at, ingested_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, ?, ?)`,
		r.ID, r.LogicalID, string(r.Type), r.Title, r.Body, string(r.Status), r.Version,
		r.OccurredAt.Format(time.RFC3339Nano), r.IngestedAt.Format(time.RFC3339Nano),
		r.CreatedAt.Format(time.RFC3339Nano), r.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return domain.Node{}, fmt.Errorf("repository: insert node version: %w", err)
	}
	// …and index the new live version.
	if err := insertNodeFTS(ctx, tx, r); err != nil {
		return domain.Node{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Node{}, fmt.Errorf("repository: commit: %w", err)
	}
	return r, nil
}

// GetNode returns a node by id (any version in the chain), ErrNotFound when
// it doesn't exist.
func (s *Service) GetNode(ctx context.Context, id string) (domain.Node, error) {
	if id == "" {
		return domain.Node{}, fmt.Errorf("%w: node id required", ErrInvalidInput)
	}
	n, err := scanNode(s.db.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes n WHERE n.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, fmt.Errorf("%w: node %s", ErrNotFound, id)
	}
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: get node: %w", err)
	}
	return n, nil
}

// GetLiveNodeByLogicalID returns the current (is_current=1) version of a
// node chain, ErrNotFound when the chain doesn't exist or has been entirely
// superseded away.
func (s *Service) GetLiveNodeByLogicalID(ctx context.Context, logicalID string) (domain.Node, error) {
	if logicalID == "" {
		return domain.Node{}, fmt.Errorf("%w: logical id required", ErrInvalidInput)
	}
	n, err := scanNode(s.db.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes n WHERE n.logical_id = ? AND n.is_current = 1`, logicalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, fmt.Errorf("%w: live node for logical_id %s", ErrNotFound, logicalID)
	}
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: get live node: %w", err)
	}
	return n, nil
}

// SearchNodes runs an FTS5 full-text query over the live node layer, best
// matches first (bm25 — lower score = closer match). The raw query is
// sanitised into a safe FTS5 expression (ftsMatchQuery) so arbitrary user text
// can't trigger an FTS5 syntax error or inject operators. A query with no
// usable term returns an empty slice (not an error). limit <= 0 falls back to
// defaultSearchLimit.
//
// nodes_fts contains is_current=1 rows only (maintenance contract), so the join
// already yields live nodes; the explicit n.is_current = 1 is belt-and-braces.
func (s *Service) SearchNodes(ctx context.Context, query string, limit int) ([]domain.Node, error) {
	match := ftsMatchQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	// Top-K happens INSIDE the FTS table (ORDER BY rank LIMIT in the subquery)
	// so FTS5 returns only the K best node_ids by its native rank order, then
	// we join those few rows back to nodes. Ordering by bm25 in the outer join
	// instead makes SQLite materialise every match into a temp b-tree before
	// the LIMIT — wasteful on a large corpus with a broad query. `rank` is the
	// FTS5 built-in (= bm25 with default weights); lower = closer match.
	// nodes_fts holds is_current=1 rows only (maintenance contract), so the K
	// inner rows are already all live; the outer n.is_current=1 is belt-and-braces.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+nodeColumns+`
		   FROM nodes n
		   JOIN (
		     SELECT node_id, rank
		       FROM nodes_fts
		      WHERE nodes_fts MATCH ?
		      ORDER BY rank
		      LIMIT ?
		   ) fts ON n.id = fts.node_id
		  WHERE n.is_current = 1
		  ORDER BY fts.rank`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("repository: search nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanNodeRows(rows)
}

// ftsMatchQuery turns arbitrary user text into a safe FTS5 MATCH expression:
// each whitespace-separated token becomes a double-quoted string literal
// (internal double-quotes doubled per FTS5 escaping), joined by spaces
// (implicit AND). Quoting neutralises FTS5 operators and special characters, so
// untrusted input can never produce a syntax error or inject query logic.
// Returns "" when no usable token remains.
func ftsMatchQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}

// MapEntryToNode records an entry↔node mapping. Idempotent (re-mapping the
// same pair is a no-op). Both ids must resolve to live rows — a typo returns
// ErrNotFound rather than silently skipping (INSERT OR IGNORE would swallow
// an FK violation, so we validate existence first).
func (s *Service) MapEntryToNode(ctx context.Context, entryID, nodeID string) error {
	if entryID == "" || nodeID == "" {
		return fmt.Errorf("%w: entry id and node id required", ErrInvalidInput)
	}
	if err := s.assertExists(ctx, "entries", entryID); err != nil {
		return fmt.Errorf("entry: %w", err)
	}
	if err := s.assertExists(ctx, "nodes", nodeID); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO entry_node_map (entry_id, node_id) VALUES (?, ?)`,
		entryID, nodeID); err != nil {
		return fmt.Errorf("repository: map entry→node: %w", err)
	}
	return nil
}

// NodesForEntry returns the nodes an entry maps to (1 today; N after split),
// created_at ascending for stable rendering.
func (s *Service) NodesForEntry(ctx context.Context, entryID string) ([]domain.Node, error) {
	if entryID == "" {
		return nil, fmt.Errorf("%w: entry id required", ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+nodeColumns+`
		   FROM entry_node_map m
		   JOIN nodes n ON n.id = m.node_id
		  WHERE m.entry_id = ?
		  ORDER BY n.created_at`, entryID)
	if err != nil {
		return nil, fmt.Errorf("repository: nodes for entry: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanNodeRows(rows)
}

// EntriesForNode returns the entries that map to a node (1 today; N after
// aggregate). Reuses the bitemporal entry column list + scanner so the
// returned entries carry the full shape (incl. supersede/version) callers
// expect.
func (s *Service) EntriesForNode(ctx context.Context, nodeID string) ([]domain.Entry, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("%w: node id required", ErrInvalidInput)
	}
	q := `
SELECT ` + labelEntryColumns + `
  FROM entry_node_map m
  JOIN entries e ON e.id = m.entry_id
 WHERE m.node_id = ?
 ORDER BY e.created_at`
	rows, err := s.db.QueryContext(ctx, q, nodeID)
	if err != nil {
		return nil, fmt.Errorf("repository: entries for node: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEntryRows(rows)
}

// CountNodes returns the total node count — diagnostics + backfill assertions.
func (s *Service) CountNodes(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("repository: count nodes: %w", err)
	}
	return n, nil
}

// assertExists returns ErrNotFound when no row with the given id exists in
// table. table is a trusted internal constant (never user input), so the
// interpolation is safe.
func (s *Service) assertExists(ctx context.Context, table, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+table+` WHERE id = ? LIMIT 1`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, table, id)
	}
	if err != nil {
		return fmt.Errorf("repository: assert %s %s: %w", table, id, err)
	}
	return nil
}

// nodeRowScanner is satisfied by both *sql.Row and *sql.Rows so scanNode
// serves single-row (GetNode) and multi-row (scanNodeRows) callers.
type nodeRowScanner interface{ Scan(dest ...any) error }

// scanNode reads one node row in nodeColumns order.
func scanNode(sc nodeRowScanner) (domain.Node, error) {
	var n domain.Node
	var typ, status, logical, supBy, occurred, ingested, created, updated string
	var version, isCur int
	if err := sc.Scan(
		&n.ID, &logical, &typ, &n.Title, &n.Body, &status,
		&version, &isCur, &supBy, &occurred, &ingested, &created, &updated,
	); err != nil {
		return domain.Node{}, err
	}
	n.LogicalID = logical
	n.Type = domain.EntryType(typ)
	n.Status = domain.Status(status)
	n.Version = version
	n.IsCurrent = isCur == 1
	n.SupersededBy = supBy
	var err error
	if n.OccurredAt, err = parseRFC(occurred); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s occurred_at: %w", n.ID, err)
	}
	if n.IngestedAt, err = parseRFC(ingested); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s ingested_at: %w", n.ID, err)
	}
	if n.CreatedAt, err = parseRFC(created); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s created_at: %w", n.ID, err)
	}
	if n.UpdatedAt, err = parseRFC(updated); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s updated_at: %w", n.ID, err)
	}
	return n, nil
}

// scanNodeRows is the shared multi-row scan for node queries.
func scanNodeRows(rows *sql.Rows) ([]domain.Node, error) {
	var out []domain.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("repository: scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
