package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/ports/ai"
	"github.com/Leon180/workingbad/internal/repository/sqlcdb"
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
	qtx := s.q.WithTx(tx)

	// 1) Idempotent on sha.
	existingChangeID, err := qtx.GetChangeIDBySHA(ctx, rc.SHA)
	if err == nil {
		change, lerr := qtx.GetRawChange(ctx, existingChangeID)
		if lerr != nil {
			return domain.RawChange{}, fmt.Errorf("repository: load raw_change: %w", lerr)
		}
		if cerr := tx.Commit(); cerr != nil {
			return domain.RawChange{}, cerr
		}
		return rawChangeFromSqlc(change), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.RawChange{}, fmt.Errorf("repository: lookup sha: %w", err)
	}

	// 2) Find-or-create change_id by (repo_id, patchID).
	var change domain.RawChange
	change.RepoID = rc.RepoID
	change.PatchID = patchID

	if patchID != "" {
		existing, err := qtx.GetChangeIDByPatch(ctx, sqlcdb.GetChangeIDByPatchParams{
			RepoID: rc.RepoID, PatchID: stringToNS(patchID),
		})
		switch {
		case err == nil:
			change.ChangeID = existing
			if err := qtx.FlipPriorSHAOnChange(ctx, sqlcdb.FlipPriorSHAOnChangeParams{
				SupersededBy: stringToNS(rc.SHA),
				ChangeID:     existing,
				Sha:          rc.SHA,
			}); err != nil {
				return domain.RawChange{}, fmt.Errorf("repository: flip rewrite is_current: %w", err)
			}
		case errors.Is(err, sql.ErrNoRows):
			// fall through to create
		default:
			return domain.RawChange{}, fmt.Errorf("repository: lookup patch_id: %w", err)
		}
	}

	if change.ChangeID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return domain.RawChange{}, fmt.Errorf("repository: gen change_id: %w", err)
		}
		change.ChangeID = id.String()
		change.CreatedAt = time.Now().UTC()
		if err := qtx.InsertRawChange(ctx, sqlcdb.InsertRawChangeParams{
			ChangeID:  change.ChangeID,
			RepoID:    change.RepoID,
			PatchID:   stringToNS(change.PatchID),
			CreatedAt: formatRFC(change.CreatedAt),
		}); err != nil {
			return domain.RawChange{}, fmt.Errorf("repository: insert raw_change: %w", err)
		}
	} else {
		// Loaded existing change — re-populate fields for a consistent return value.
		ch, err := qtx.GetRawChange(ctx, change.ChangeID)
		if err != nil {
			return domain.RawChange{}, err
		}
		change = rawChangeFromSqlc(ch)
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
	if err := qtx.InsertRawCommit(ctx, sqlcdb.InsertRawCommitParams{
		Sha:          rc.SHA,
		RepoID:       rc.RepoID,
		ChangeID:     change.ChangeID,
		ParentShas:   parents,
		Author:       rc.Author,
		AuthorTime:   formatRFC(rc.AuthorTime),
		Committer:    rc.Committer,
		CommitTime:   formatRFC(rc.CommitTime),
		Message:      rc.Message,
		Diff:         stringToNS(rc.Diff),
		BranchHint:   stringToNS(rc.BranchHint),
		IsCurrent:    1,
		SupersededBy: stringToNS(rc.SupersededBy),
		CreatedAt:    formatRFC(rc.CreatedAt),
	}); err != nil {
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
	qtx := s.q.WithTx(tx)

	existing, err := qtx.GetSegmentByKey(ctx, sqlcdb.GetSegmentByKeyParams{
		RepoID: seg.RepoID, Source: string(seg.Source), SourceRef: seg.SourceRef,
	})

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
		if err := qtx.InsertSegment(ctx, sqlcdb.InsertSegmentParams{
			ID:            seg.ID,
			RepoID:        seg.RepoID,
			Source:        string(seg.Source),
			SourceRef:     seg.SourceRef,
			SummaryState:  string(seg.SummaryState),
			AnchorPatchID: stringToNS(seg.AnchorPatchID),
			Metadata:      seg.Metadata,
			CreatedAt:     formatRFC(seg.CreatedAt),
			UpdatedAt:     formatRFC(seg.UpdatedAt),
		}); err != nil {
			return domain.Segment{}, fmt.Errorf("repository: insert segment: %w", err)
		}
	case err != nil:
		return domain.Segment{}, err
	default:
		seg.ID = existing.ID
		if seg.CreatedAt.IsZero() {
			seg.CreatedAt = parseRFC(existing.CreatedAt)
		}
		metaArg := seg.Metadata
		if metaArg == "" {
			metaArg = "{}"
		}
		if err := qtx.UpdateSegment(ctx, sqlcdb.UpdateSegmentParams{
			SummaryState:  string(seg.SummaryState),
			AnchorPatchID: stringToNS(seg.AnchorPatchID),
			Metadata:      metaArg,
			UpdatedAt:     formatRFC(seg.UpdatedAt),
			ID:            seg.ID,
		}); err != nil {
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
	if err := s.q.LinkSegmentRaw(ctx, sqlcdb.LinkSegmentRawParams{
		SegmentID: segmentID, ChangeID: changeID,
	}); err != nil {
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
	qtx := s.q.WithTx(tx)

	rawRows, err := qtx.GetCurrentChangesForSegment(ctx, seg.ID)
	if err != nil {
		return fmt.Errorf("repository: load segment changes: %w", err)
	}
	if len(rawRows) == 0 {
		return errors.New("repository: segment has no is_current raw_changes")
	}
	changes := make([]domain.RawChange, len(rawRows))
	for i, r := range rawRows {
		changes[i] = domain.RawChange{
			ChangeID:  r.ChangeID,
			RepoID:    r.RepoID,
			PatchID:   nsToString(r.PatchID),
			CreatedAt: parseRFC(r.CreatedAt),
		}
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

	existingID, err := qtx.GetLiveActivityForSegment(ctx, stringToNS(seg.SourceRef))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.insertEntryInTx(ctx, qtx, &entry); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := s.supersedeEntryInTx(ctx, qtx, existingID, &entry); err != nil {
			return err
		}
	}

	if err := qtx.MarkSegmentMaterialized(ctx, sqlcdb.MarkSegmentMaterializedParams{
		SummaryState: string(domain.SummaryStateMaterialized),
		UpdatedAt:    formatRFC(time.Now().UTC()),
		ID:           seg.ID,
	}); err != nil {
		return fmt.Errorf("repository: mark materialized: %w", err)
	}

	return tx.Commit()
}

// insertEntryInTx writes a fresh Entry + its FTS mirror inside an existing tx.
func (s *Service) insertEntryInTx(ctx context.Context, qtx *sqlcdb.Queries, e *domain.Entry) error {
	if err := assignNewIDs(e); err != nil {
		return err
	}
	stampTimes(e, time.Now().UTC())
	e.IsCurrent = true
	if err := qtx.InsertEntryRow(ctx, entryToInsertParams(*e)); err != nil {
		return fmt.Errorf("repository: insert entry: %w", err)
	}
	if err := qtx.InsertEntryFTS(ctx, sqlcdb.InsertEntryFTSParams{
		EntryID: e.ID, Title: e.Title, Body: e.Body,
	}); err != nil {
		return fmt.Errorf("repository: fts insert: %w", err)
	}
	return nil
}

// supersedeEntryInTx flips an existing is_current=1 entry to superseded,
// removes its FTS row, and writes the replacement inside the same tx.
func (s *Service) supersedeEntryInTx(ctx context.Context, qtx *sqlcdb.Queries, oldID string, replacement *domain.Entry) error {
	old, err := qtx.GetEntryByID(ctx, oldID)
	if err != nil {
		return fmt.Errorf("repository: load supersede target: %w", err)
	}
	if err := assignNewIDs(replacement); err != nil {
		return err
	}
	replacement.LogicalID = old.LogicalID
	now := time.Now().UTC()
	stampTimes(replacement, now)
	replacement.IsCurrent = true

	if err := qtx.FlipEntrySuperseded(ctx, sqlcdb.FlipEntrySupersededParams{
		SupersededBy: stringToNS(replacement.ID),
		UpdatedAt:    formatRFC(now),
		ID:           oldID,
	}); err != nil {
		return fmt.Errorf("repository: flip superseded: %w", err)
	}
	if err := qtx.DeleteEntryFTS(ctx, oldID); err != nil {
		return fmt.Errorf("repository: drop superseded fts: %w", err)
	}
	if err := qtx.InsertEntryRow(ctx, entryToInsertParams(*replacement)); err != nil {
		return fmt.Errorf("repository: insert replacement: %w", err)
	}
	if err := qtx.InsertEntryFTS(ctx, sqlcdb.InsertEntryFTSParams{
		EntryID: replacement.ID, Title: replacement.Title, Body: replacement.Body,
	}); err != nil {
		return fmt.Errorf("repository: fts insert replacement: %w", err)
	}
	return nil
}

// listSegmentsNeedingMaterialize uses hand-written SQL because the optional
// repo filter does not slot cleanly into a sqlc query. Two specific queries
// would also work; this is simpler.
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
		seg.CreatedAt = parseRFC(created)
		seg.UpdatedAt = parseRFC(updated)
		out = append(out, seg)
	}
	return out, rows.Err()
}
