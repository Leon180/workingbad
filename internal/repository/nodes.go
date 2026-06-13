package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/domain"
)

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
// Kept in one place so GetNode / NodesForEntry / GetLiveNodeByLogicalID stay
// in lockstep with the scanner. COALESCE bridges D1 rows (added before the
// D2 supersede columns existed) — version→1, occurred/ingested→created_at.
const nodeColumns = `id, COALESCE(logical_id, id), type, title, body, COALESCE(status, ''),
       COALESCE(version, 1), is_current, COALESCE(superseded_by, ''),
       COALESCE(occurred_at, ingested_at, created_at),
       COALESCE(ingested_at, created_at),
       created_at, updated_at`

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

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes
		   (id, logical_id, type, title, body, status, version, is_current,
		    superseded_by, occurred_at, ingested_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, ?, ?)`,
		n.ID, n.LogicalID, string(n.Type), n.Title, n.Body, string(n.Status), n.Version,
		n.OccurredAt.Format(time.RFC3339Nano), n.IngestedAt.Format(time.RFC3339Nano),
		n.CreatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: insert node: %w", err)
	}
	return n, nil
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
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET is_current = 0, superseded_by = ?, updated_at = ? WHERE id = ?`,
		r.ID, now.Format(time.RFC3339Nano), oldID); err != nil {
		return domain.Node{}, fmt.Errorf("repository: retire node %s: %w", oldID, err)
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
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id))
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
		`SELECT `+nodeColumns+` FROM nodes WHERE logical_id = ? AND is_current = 1`, logicalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, fmt.Errorf("%w: live node for logical_id %s", ErrNotFound, logicalID)
	}
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: get live node: %w", err)
	}
	return n, nil
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
		  ORDER BY created_at`, entryID)
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
