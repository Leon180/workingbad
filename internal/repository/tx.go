package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Leon180/workingbad/internal/domain"
)

// dbtx is the read+write subset shared by *sql.DB and *sql.Tx. A write "core"
// (createNodeTx, mapEntryToNodeTx, …) takes a dbtx so the same logic serves a
// standalone call and a composed pipeline step. Cores that must be atomic
// across multiple statements (node row + its FTS mirror) are only ever called
// with a *sql.Tx (via Tx below) — never s.db directly.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx is a transaction-scoped handle exposing the graph-write operations the LLM
// pipeline composes atomically (split = 1 entry → N nodes + N maps; aggregate =
// N entries → 1 node + N maps). Obtained via Service.InTx. Each method mirrors
// the same-named Service method but participates in the caller's transaction
// instead of opening its own — so a multi-step split/aggregate commits or rolls
// back as a unit.
type Tx struct {
	tx *sql.Tx
}

// txKey marks a context as already inside an InTx, so a nested InTx fails fast
// instead of deadlocking (see InTx).
type txKey struct{}

// InTx runs fn inside a single transaction: every write fn performs commits
// together or rolls back together. This is the atomic-composition seam the
// Slice-F pipeline orchestrator builds on — standalone Service writes each open
// their own tx, so without this a split that writes N nodes + N mappings would
// not be atomic. fn's error rolls the tx back and is returned unwrapped (so
// callers can errors.Is it); a commit failure is returned wrapped.
//
// CRITICAL — single-connection discipline: the DB pool is MaxOpenConns(1)
// (db.go), and InTx holds that one connection for fn's whole duration. So
// inside fn you must use ONLY the *Tx handle; calling any Service method
// (s.GetNode, s.CreateNode, …) would try to acquire a second connection and
// deadlock. Do reads BEFORE InTx and pass the values in, or read via *Tx.
// Calling InTx within InTx is caught here and returned as an error rather than
// hanging; the s.* read case can't be caught cheaply, hence this contract.
func (s *Service) InTx(ctx context.Context, fn func(context.Context, *Tx) error) error {
	if ctx.Value(txKey{}) != nil {
		return errors.New("repository: InTx called within InTx — would deadlock on the single-connection pool; compose all writes inside one InTx via *Tx")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(context.WithValue(ctx, txKey{}, struct{}{}), &Tx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repository: commit: %w", err)
	}
	return nil
}

// CreateNode creates a node within the transaction (see Service.CreateNode for
// the full contract).
func (t *Tx) CreateNode(ctx context.Context, n domain.Node) (domain.Node, error) {
	return createNodeTx(ctx, t.tx, n)
}

// MapEntryToNode records an entry↔node mapping within the transaction (see
// Service.MapEntryToNode).
func (t *Tx) MapEntryToNode(ctx context.Context, entryID, nodeID string) error {
	return mapEntryToNodeTx(ctx, t.tx, entryID, nodeID)
}
