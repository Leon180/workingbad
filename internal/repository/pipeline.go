package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/ai"
	"github.com/Leon180/workingbad/internal/domain"
)

// UpsertRaw writes a git commit + its associated logical RawChange in one
// transaction. Idempotent on sha; rewrite-aware via patch-id.
//
// patchID is supplied separately because it lives on the RawChange (logical
// layer) rather than on the RawCommit (physical layer). Pass "" for merge
// commits or when patch-id computation failed.
//
// Semantics
//   - sha already exists → no-op; returns the existing RawChange.
//   - sha is new and (repo_id, patchID) maps to an existing change_id →
//     reuse it (amend/rebase rewrite path); flip the previously-current sha
//     on that change to is_current=0, superseded_by = new sha.
//   - sha is new and patchID is empty or unseen → create a fresh change_id.
func (s *Service) UpsertRaw(ctx context.Context, rc domain.RawCommit, patchID string) (domain.RawChange, error) {
	if rc.SHA == "" {
		return domain.RawChange{}, errors.New("repository: UpsertRaw requires sha")
	}
	if rc.RepoID == "" {
		return domain.RawChange{}, errors.New("repository: UpsertRaw requires repo_id")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RawChange{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1) Idempotent on sha.
	if existingChangeID, ok, err := selectExistingChangeBySHA(ctx, tx, rc.SHA); err != nil {
		return domain.RawChange{}, err
	} else if ok {
		change, err := loadRawChange(ctx, tx, existingChangeID)
		if err != nil {
			return domain.RawChange{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.RawChange{}, err
		}
		return change, nil
	}

	// 2) Find-or-create change_id by (repo_id, patchID).
	var change domain.RawChange
	change.RepoID = rc.RepoID
	change.PatchID = patchID

	if patchID != "" {
		if existing, ok, err := selectChangeByPatch(ctx, tx, rc.RepoID, patchID); err != nil {
			return domain.RawChange{}, err
		} else if ok {
			change.ChangeID = existing
			// Rewrite path — flip prior is_current=1 sha on this change.
			if _, err := tx.ExecContext(ctx,
				`UPDATE raw_commits SET is_current = 0, superseded_by = ? WHERE change_id = ? AND is_current = 1 AND sha != ?`,
				rc.SHA, existing, rc.SHA,
			); err != nil {
				return domain.RawChange{}, fmt.Errorf("repository: flip rewrite is_current: %w", err)
			}
		}
	}

	if change.ChangeID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return domain.RawChange{}, fmt.Errorf("repository: gen change_id: %w", err)
		}
		change.ChangeID = id.String()
		change.CreatedAt = time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO raw_changes (change_id, repo_id, patch_id, created_at) VALUES (?, ?, ?, ?)`,
			change.ChangeID, change.RepoID, nullableString(change.PatchID),
			change.CreatedAt.Format(time.RFC3339Nano),
		); err != nil {
			return domain.RawChange{}, fmt.Errorf("repository: insert raw_change: %w", err)
		}
	} else {
		// Loaded existing change — re-populate fields for a consistent return value.
		ch, err := loadRawChange(ctx, tx, change.ChangeID)
		if err != nil {
			return domain.RawChange{}, err
		}
		change = ch
	}

	// 3) Insert raw_commits.
	if rc.CreatedAt.IsZero() {
		rc.CreatedAt = time.Now().UTC()
	}
	parents := "[]"
	if len(rc.ParentSHAs) > 0 {
		b, err := json.Marshal(rc.ParentSHAs)
		if err != nil {
			return domain.RawChange{}, err
		}
		parents = string(b)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO raw_commits
		   (sha, repo_id, change_id, parent_shas, author, author_time, committer, commit_time, message, diff, branch_hint, is_current, superseded_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rc.SHA, rc.RepoID, change.ChangeID, parents,
		rc.Author, formatTime(rc.AuthorTime),
		rc.Committer, formatTime(rc.CommitTime),
		rc.Message, nullableString(rc.Diff), nullableString(rc.BranchHint),
		1, nullableString(rc.SupersededBy),
		rc.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return domain.RawChange{}, fmt.Errorf("repository: insert raw_commit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.RawChange{}, err
	}
	return change, nil
}

// UpsertSegment writes a segment row idempotent on (repo_id, source,
// source_ref). On conflict it updates summary_state / anchor_patch_id and
// preserves created_at.
func (s *Service) UpsertSegment(ctx context.Context, seg domain.Segment) (domain.Segment, error) {
	if seg.RepoID == "" || seg.Source == "" || seg.SourceRef == "" {
		return domain.Segment{}, errors.New("repository: UpsertSegment requires repo_id, source, source_ref")
	}
	if seg.SummaryState == "" {
		seg.SummaryState = domain.SummaryStatePending
	}
	now := time.Now().UTC()
	seg.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Segment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var existingID, existingCreated string
	err = tx.QueryRowContext(ctx,
		`SELECT id, created_at FROM segments WHERE repo_id = ? AND source = ? AND source_ref = ?`,
		seg.RepoID, string(seg.Source), seg.SourceRef,
	).Scan(&existingID, &existingCreated)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if seg.ID == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return domain.Segment{}, err
			}
			seg.ID = id.String()
		}
		if seg.CreatedAt.IsZero() {
			seg.CreatedAt = now
		}
		if seg.Metadata == "" {
			seg.Metadata = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO segments (id, repo_id, source, source_ref, summary_state, anchor_patch_id, metadata, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			seg.ID, seg.RepoID, string(seg.Source), seg.SourceRef,
			string(seg.SummaryState), nullableString(seg.AnchorPatchID), seg.Metadata,
			seg.CreatedAt.Format(time.RFC3339Nano), seg.UpdatedAt.Format(time.RFC3339Nano),
		); err != nil {
			return domain.Segment{}, fmt.Errorf("repository: insert segment: %w", err)
		}
	case err != nil:
		return domain.Segment{}, err
	default:
		seg.ID = existingID
		if seg.CreatedAt.IsZero() {
			seg.CreatedAt, _ = time.Parse(time.RFC3339Nano, existingCreated)
		}
		metaArg := seg.Metadata
		if metaArg == "" {
			metaArg = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE segments SET summary_state = ?, anchor_patch_id = ?, metadata = ?, updated_at = ? WHERE id = ?`,
			string(seg.SummaryState), nullableString(seg.AnchorPatchID),
			metaArg, seg.UpdatedAt.Format(time.RFC3339Nano), seg.ID,
		); err != nil {
			return domain.Segment{}, fmt.Errorf("repository: update segment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return domain.Segment{}, err
	}
	return seg, nil
}

// LinkSegmentRaw records the (segment, change) link. Idempotent on the PK.
func (s *Service) LinkSegmentRaw(ctx context.Context, segmentID, changeID string) error {
	if segmentID == "" || changeID == "" {
		return errors.New("repository: LinkSegmentRaw requires segment_id and change_id")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO segment_raw (segment_id, change_id) VALUES (?, ?)`,
		segmentID, changeID,
	); err != nil {
		return fmt.Errorf("repository: link segment_raw: %w", err)
	}
	return nil
}

// MaterializeScope filters which segments BatchMaterialize processes.
// Empty fields are wildcards.
type MaterializeScope struct {
	RepoID string
}

// MaterializeResult summarises one BatchMaterialize call.
type MaterializeResult struct {
	Materialized int
	Failed       int
	Errors       []error
}

// BatchMaterialize runs the lazy materialize loop:
//   - Each pending|stale segment in scope is processed in its own tx.
//   - Per segment: load is_current raw_changes, call provider.Summarize,
//     InsertEntry-or-Supersede the activity, mark the segment materialized.
//   - One segment failing leaves it pending/stale; the next continues.
//
// The first returned error covers infrastructure-level failures
// (e.g. listing segments); per-segment failures go into Result.Errors.
func (s *Service) BatchMaterialize(ctx context.Context, scope MaterializeScope, provider ai.AIProvider) (MaterializeResult, error) {
	if provider == nil {
		return MaterializeResult{}, errors.New("repository: BatchMaterialize requires an AIProvider")
	}
	segments, err := s.listSegmentsNeedingMaterialize(ctx, scope)
	if err != nil {
		return MaterializeResult{}, err
	}
	var result MaterializeResult
	for _, seg := range segments {
		if err := s.materializeOne(ctx, seg, provider); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Errorf("segment %s: %w", seg.ID, err))
			continue
		}
		result.Materialized++
	}
	return result, nil
}

func (s *Service) materializeOne(ctx context.Context, seg domain.Segment, provider ai.AIProvider) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	changes, err := loadCurrentChangesForSegment(ctx, tx, seg.ID)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return errors.New("repository: segment has no is_current raw_changes")
	}

	title, body, err := provider.Summarize(ctx, changes)
	if err != nil {
		return fmt.Errorf("provider.Summarize: %w", err)
	}

	entry := domain.Entry{
		Type:      domain.EntryTypeActivity,
		Title:     title,
		Body:      body,
		Source:    domain.SourceGit,
		SourceRef: seg.SourceRef,
		Origin:    domain.OriginLocal,
		RepoID:    seg.RepoID,
	}
	if err := validateEntry(entry); err != nil {
		return err
	}

	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM entries WHERE type = 'activity' AND source = 'git' AND source_ref = ? AND is_current = 1 LIMIT 1`,
		seg.SourceRef,
	).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.insertEntryInTx(ctx, tx, &entry); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := s.supersedeEntryInTx(ctx, tx, existingID, &entry); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE segments SET summary_state = ?, updated_at = ? WHERE id = ?`,
		string(domain.SummaryStateMaterialized), time.Now().UTC().Format(time.RFC3339Nano), seg.ID,
	); err != nil {
		return fmt.Errorf("repository: mark materialized: %w", err)
	}

	return tx.Commit()
}

// insertEntryInTx writes a fresh Entry + its FTS mirror inside an existing tx.
// Used by BatchMaterialize where the segment update and entry write must
// commit atomically.
func (s *Service) insertEntryInTx(ctx context.Context, tx *sql.Tx, e *domain.Entry) error {
	if err := assignNewIDs(e); err != nil {
		return err
	}
	stampTimes(e, time.Now().UTC())
	e.IsCurrent = true
	if err := insertEntryRow(ctx, tx, *e); err != nil {
		return err
	}
	return insertFTS(ctx, tx, *e)
}

// supersedeEntryInTx flips an existing is_current=1 entry to superseded,
// removes its FTS row, and writes the replacement inside the same tx. The
// replacement inherits LogicalID from the old entry — identity invariance
// across re-materialize.
func (s *Service) supersedeEntryInTx(ctx context.Context, tx *sql.Tx, oldID string, replacement *domain.Entry) error {
	var oldLogicalID string
	if err := tx.QueryRowContext(ctx,
		`SELECT logical_id FROM entries WHERE id = ? AND is_current = 1`, oldID,
	).Scan(&oldLogicalID); err != nil {
		return fmt.Errorf("repository: load supersede target: %w", err)
	}
	if err := assignNewIDs(replacement); err != nil {
		return err
	}
	replacement.LogicalID = oldLogicalID
	now := time.Now().UTC()
	stampTimes(replacement, now)
	replacement.IsCurrent = true

	if _, err := tx.ExecContext(ctx,
		`UPDATE entries SET is_current = 0, superseded_by = ?, updated_at = ? WHERE id = ?`,
		replacement.ID, now.Format(time.RFC3339Nano), oldID,
	); err != nil {
		return fmt.Errorf("repository: flip superseded: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries_fts WHERE entry_id = ?`, oldID); err != nil {
		return fmt.Errorf("repository: drop superseded fts: %w", err)
	}
	if err := insertEntryRow(ctx, tx, *replacement); err != nil {
		return err
	}
	return insertFTS(ctx, tx, *replacement)
}

func (s *Service) listSegmentsNeedingMaterialize(ctx context.Context, scope MaterializeScope) ([]domain.Segment, error) {
	args := []any{string(domain.SummaryStatePending), string(domain.SummaryStateStale)}
	q := `SELECT id, repo_id, source, source_ref, summary_state, COALESCE(anchor_patch_id, ''), metadata, created_at, updated_at
	        FROM segments
	       WHERE summary_state IN (?, ?)`
	if scope.RepoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, scope.RepoID)
	}
	q += ` ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list segments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Segment
	for rows.Next() {
		var seg domain.Segment
		var src, state, created, updated string
		if err := rows.Scan(&seg.ID, &seg.RepoID, &src, &seg.SourceRef, &state, &seg.AnchorPatchID, &seg.Metadata, &created, &updated); err != nil {
			return nil, err
		}
		seg.Source = domain.Source(src)
		seg.SummaryState = domain.SummaryState(state)
		seg.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		seg.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, seg)
	}
	return out, rows.Err()
}

func loadCurrentChangesForSegment(ctx context.Context, tx *sql.Tx, segmentID string) ([]domain.RawChange, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT rc.change_id, rc.repo_id, COALESCE(rc.patch_id, ''), rc.created_at
		   FROM segment_raw sr
		   JOIN raw_changes rc ON rc.change_id = sr.change_id
		  WHERE sr.segment_id = ?
		    AND EXISTS (SELECT 1 FROM raw_commits c WHERE c.change_id = rc.change_id AND c.is_current = 1)
		  ORDER BY rc.created_at ASC`,
		segmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("repository: load segment changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.RawChange
	for rows.Next() {
		var c domain.RawChange
		var created string
		if err := rows.Scan(&c.ChangeID, &c.RepoID, &c.PatchID, &created); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func selectExistingChangeBySHA(ctx context.Context, tx *sql.Tx, sha string) (string, bool, error) {
	var changeID string
	err := tx.QueryRowContext(ctx, `SELECT change_id FROM raw_commits WHERE sha = ?`, sha).Scan(&changeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("repository: lookup sha: %w", err)
	}
	return changeID, true, nil
}

func selectChangeByPatch(ctx context.Context, tx *sql.Tx, repoID, patchID string) (string, bool, error) {
	var changeID string
	err := tx.QueryRowContext(ctx,
		`SELECT change_id FROM raw_changes WHERE repo_id = ? AND patch_id = ?`,
		repoID, patchID,
	).Scan(&changeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("repository: lookup patch_id: %w", err)
	}
	return changeID, true, nil
}

func loadRawChange(ctx context.Context, tx *sql.Tx, changeID string) (domain.RawChange, error) {
	var c domain.RawChange
	var created string
	if err := tx.QueryRowContext(ctx,
		`SELECT change_id, repo_id, COALESCE(patch_id, ''), created_at FROM raw_changes WHERE change_id = ?`,
		changeID,
	).Scan(&c.ChangeID, &c.RepoID, &c.PatchID, &created); err != nil {
		return domain.RawChange{}, fmt.Errorf("repository: load raw_change: %w", err)
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return c, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return t.UTC().Format(time.RFC3339Nano)
}
