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

// CreateNode inserts a graph node and returns it with id/timestamps filled.
// Slice D1: the migration backfills nodes from entries; this is the
// programmatic path the future split/aggregate pipeline (Slices G/H) writes
// through. Assigns a uuid v7 id when empty and stamps created_at/updated_at
// when zero. Goes through RepositoryService — the sole write gate — like
// every other mutation.
func (s *Service) CreateNode(ctx context.Context, n domain.Node) (domain.Node, error) {
	if n.Title == "" {
		return domain.Node{}, fmt.Errorf("%w: node title required", ErrInvalidInput)
	}
	if _, ok := validNodeTypes[n.Type]; !ok {
		return domain.Node{}, fmt.Errorf("%w: node type %q not in {activity,research,discuss,decision,goal}", ErrInvalidInput, n.Type)
	}
	if n.Type == domain.EntryTypeGoal && n.Status == "" {
		return domain.Node{}, fmt.Errorf("%w: goal node requires status", ErrInvalidInput)
	}
	if n.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return domain.Node{}, fmt.Errorf("repository: generate node id: %w", err)
		}
		n.ID = id.String()
	}
	now := time.Now().UTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes (id, type, title, body, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, string(n.Type), n.Title, n.Body, string(n.Status),
		n.CreatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: insert node: %w", err)
	}
	return n, nil
}

// GetNode returns a node by id, ErrNotFound when it doesn't exist.
func (s *Service) GetNode(ctx context.Context, id string) (domain.Node, error) {
	if id == "" {
		return domain.Node{}, fmt.Errorf("%w: node id required", ErrInvalidInput)
	}
	var n domain.Node
	var typ, status, created, updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, type, title, body, COALESCE(status, ''), created_at, updated_at
		   FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &typ, &n.Title, &n.Body, &status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, fmt.Errorf("%w: node %s", ErrNotFound, id)
	}
	if err != nil {
		return domain.Node{}, fmt.Errorf("repository: get node: %w", err)
	}
	n.Type = domain.EntryType(typ)
	n.Status = domain.Status(status)
	if n.CreatedAt, err = parseRFC(created); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s created_at: %w", id, err)
	}
	if n.UpdatedAt, err = parseRFC(updated); err != nil {
		return domain.Node{}, fmt.Errorf("repository: parse node %s updated_at: %w", id, err)
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
		`SELECT n.id, n.type, n.title, n.body, COALESCE(n.status, ''), n.created_at, n.updated_at
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

// scanNodeRows is the shared row-scan for node queries.
func scanNodeRows(rows *sql.Rows) ([]domain.Node, error) {
	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		var typ, status, created, updated string
		if err := rows.Scan(&n.ID, &typ, &n.Title, &n.Body, &status, &created, &updated); err != nil {
			return nil, fmt.Errorf("repository: scan node: %w", err)
		}
		n.Type = domain.EntryType(typ)
		n.Status = domain.Status(status)
		var err error
		if n.CreatedAt, err = parseRFC(created); err != nil {
			return nil, fmt.Errorf("repository: parse node %s created_at: %w", n.ID, err)
		}
		if n.UpdatedAt, err = parseRFC(updated); err != nil {
			return nil, fmt.Errorf("repository: parse node %s updated_at: %w", n.ID, err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
