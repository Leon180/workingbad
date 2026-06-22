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
	// A git commit without an author time is invalid data, not recoverable.
	// The previous formatRFC silent-now fallback masked this; making it loud
	// here closes the path that would have written ingestion time into the
	// author_time column (which downstream bitemporal work reads as event time).
	if rc.AuthorTime.IsZero() {
		return domain.RawChange{}, errors.New("repository: UpsertRaw requires non-zero author_time")
	}
	if rc.CommitTime.IsZero() {
		return domain.RawChange{}, errors.New("repository: UpsertRaw requires non-zero commit_time")
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
		converted, cerr := rawChangeFromSqlc(change)
		if cerr != nil {
			return domain.RawChange{}, cerr
		}
		return converted, nil
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
		change.IngestedAt = time.Now().UTC()
		ingestedRFC, ferr := formatRFC(change.IngestedAt)
		if ferr != nil {
			return domain.RawChange{}, fmt.Errorf("repository: format change ingested_at: %w", ferr)
		}
		if err := qtx.InsertRawChange(ctx, sqlcdb.InsertRawChangeParams{
			ChangeID:   change.ChangeID,
			RepoID:     change.RepoID,
			PatchID:    stringToNS(change.PatchID),
			IngestedAt: stringToNS(ingestedRFC),
			CreatedAt:  ingestedRFC,
		}); err != nil {
			return domain.RawChange{}, fmt.Errorf("repository: insert raw_change: %w", err)
		}
	} else {
		// Loaded existing change — re-populate fields for a consistent return value.
		ch, err := qtx.GetRawChange(ctx, change.ChangeID)
		if err != nil {
			return domain.RawChange{}, err
		}
		change, err = rawChangeFromSqlc(ch)
		if err != nil {
			return domain.RawChange{}, err
		}
	}

	// 3) Insert raw_commits.
	if rc.IngestedAt.IsZero() {
		rc.IngestedAt = time.Now().UTC()
	}
	parents := "[]"
	if len(rc.ParentSHAs) > 0 {
		b, err := json.Marshal(rc.ParentSHAs)
		if err != nil {
			return domain.RawChange{}, err
		}
		parents = string(b)
	}
	authorRFC, err := formatRFC(rc.AuthorTime)
	if err != nil {
		return domain.RawChange{}, fmt.Errorf("repository: format author_time: %w", err)
	}
	commitRFC, err := formatRFC(rc.CommitTime)
	if err != nil {
		return domain.RawChange{}, fmt.Errorf("repository: format commit_time: %w", err)
	}
	rcIngestedRFC, err := formatRFC(rc.IngestedAt)
	if err != nil {
		return domain.RawChange{}, fmt.Errorf("repository: format raw_commit ingested_at: %w", err)
	}
	if err := qtx.InsertRawCommit(ctx, sqlcdb.InsertRawCommitParams{
		Sha:          rc.SHA,
		RepoID:       rc.RepoID,
		ChangeID:     change.ChangeID,
		ParentShas:   parents,
		Author:       rc.Author,
		AuthorTime:   authorRFC,
		Committer:    rc.Committer,
		CommitTime:   commitRFC,
		Message:      rc.Message,
		Diff:         stringToNS(rc.Diff),
		BranchHint:   stringToNS(rc.BranchHint),
		IsCurrent:    1,
		SupersededBy: stringToNS(rc.SupersededBy),
		IngestedAt:   stringToNS(rcIngestedRFC),
		CreatedAt:    rcIngestedRFC,
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
		if seg.IngestedAt.IsZero() {
			seg.IngestedAt = now
		}
		if seg.Metadata == "" {
			seg.Metadata = "{}"
		}
		ingestedRFC, ferr := formatRFC(seg.IngestedAt)
		if ferr != nil {
			return domain.Segment{}, fmt.Errorf("repository: format segment ingested_at: %w", ferr)
		}
		updatedRFC, ferr := formatRFC(seg.UpdatedAt)
		if ferr != nil {
			return domain.Segment{}, fmt.Errorf("repository: format segment updated_at: %w", ferr)
		}
		occurredMin := optTimeToNS(seg.OccurredAtMin)
		occurredMax := optTimeToNS(seg.OccurredAtMax)
		if err := qtx.InsertSegment(ctx, sqlcdb.InsertSegmentParams{
			ID:            seg.ID,
			RepoID:        seg.RepoID,
			Source:        string(seg.Source),
			SourceRef:     seg.SourceRef,
			SummaryState:  string(seg.SummaryState),
			AnchorPatchID: stringToNS(seg.AnchorPatchID),
			Metadata:      seg.Metadata,
			OccurredAtMin: occurredMin,
			OccurredAtMax: occurredMax,
			IngestedAt:    stringToNS(ingestedRFC),
			CreatedAt:     ingestedRFC,
			UpdatedAt:     updatedRFC,
		}); err != nil {
			return domain.Segment{}, fmt.Errorf("repository: insert segment: %w", err)
		}
	case err != nil:
		return domain.Segment{}, err
	default:
		seg.ID = existing.ID
		if seg.IngestedAt.IsZero() {
			parsed, perr := parseRFCWithFallback(existing.IngestedAt, "")
			if perr != nil {
				// Existing row missing both ingested_at and any fallback; surface.
				return domain.Segment{}, fmt.Errorf("repository: parse existing segment ingested_at: %w", perr)
			}
			seg.IngestedAt = parsed
		}
		metaArg := seg.Metadata
		if metaArg == "" {
			metaArg = "{}"
		}
		updatedRFC, ferr := formatRFC(seg.UpdatedAt)
		if ferr != nil {
			return domain.Segment{}, fmt.Errorf("repository: format segment updated_at: %w", ferr)
		}
		occurredMin := optTimeToNS(seg.OccurredAtMin)
		occurredMax := optTimeToNS(seg.OccurredAtMax)
		if err := qtx.UpdateSegment(ctx, sqlcdb.UpdateSegmentParams{
			SummaryState:  string(seg.SummaryState),
			AnchorPatchID: stringToNS(seg.AnchorPatchID),
			Metadata:      metaArg,
			OccurredAtMin: occurredMin,
			OccurredAtMax: occurredMax,
			UpdatedAt:     updatedRFC,
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
	// Build the content-agnostic Summarize input. ID = ChangeID drives the
	// mock's deterministic hash. Text carries the patch fingerprint as a
	// placeholder and is empty for merge commits (PatchID NULL); real
	// commit-message/diff content is wired with the real provider (F9).
	items := make([]domain.Summarizable, len(rawRows))
	var earliestAuthor, latestAuthor time.Time
	for i, r := range rawRows {
		// Validate ingested_at parses before committing the tx — a corrupt
		// nanosecond timestamp is a storage-invariant violation. The value
		// itself is unused here (Summarizable has no IngestedAt).
		if _, perr := optNSToTime(r.IngestedAt); perr != nil {
			return fmt.Errorf("repository: parse raw_change %s ingested_at: %w", r.ChangeID, perr)
		}
		items[i] = domain.Summarizable{
			ID:   r.ChangeID,
			Text: nsToString(r.PatchID),
		}
		// earliest_author_time is computed by the SQL — capture the MIN across
		// the change set so the synthesised activity entry's occurred_at
		// anchors to the source event time, not the materialise wall clock.
		// Without this wiring (red team #1 / hunter P1), every re-summarize
		// would drift activity.occurred_at to ingest time = bitemporal
		// information loss.
		if at, ok := earliestTimeFromAny(r.EarliestAuthorTime); ok {
			if earliestAuthor.IsZero() || at.Before(earliestAuthor) {
				earliestAuthor = at
			}
			if latestAuthor.IsZero() || at.After(latestAuthor) {
				latestAuthor = at
			}
		}
	}

	title, body, err := provider.Summarize(ctx, items)
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
		// Anchor activity occurred_at to the earliest git author_time across
		// the segment's change set. Falls back to segments.occurred_at_min
		// (legacy/orphan case), then to zero — stampTimes defaults it to
		// now() if it's still zero at the write moment, with quality_degraded
		// set so the read side knows the timestamp is not source-true.
		OccurredAt: pickActivityOccurredAt(earliestAuthor, seg.OccurredAtMin),
	}
	if entry.OccurredAt.IsZero() {
		entry.QualityDegraded = true
	}
	if err := validateEntry(entry); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
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

	updatedRFC, ferr := formatRFC(time.Now().UTC())
	if ferr != nil {
		return fmt.Errorf("repository: format mark-materialized now: %w", ferr)
	}
	// Cache the work-window MIN/MAX on segments so future at-time queries
	// don't have to re-aggregate raw_commits. Skipped silently when the join
	// produced no signal (orphan segment) — column stays NULL.
	if !earliestAuthor.IsZero() {
		if err := qtx.SetSegmentTimeWindow(ctx, sqlcdb.SetSegmentTimeWindowParams{
			OccurredAtMin: optTimeToNS(earliestAuthor),
			OccurredAtMax: optTimeToNS(latestAuthor),
			UpdatedAt:     updatedRFC,
			ID:            seg.ID,
		}); err != nil {
			return fmt.Errorf("repository: cache segment time window: %w", err)
		}
	}
	if err := qtx.MarkSegmentMaterialized(ctx, sqlcdb.MarkSegmentMaterializedParams{
		SummaryState: string(domain.SummaryStateMaterialized),
		UpdatedAt:    updatedRFC,
		ID:           seg.ID,
	}); err != nil {
		return fmt.Errorf("repository: mark materialized: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repository: commit: %w", err)
	}
	return nil
}

// insertEntryInTx writes a fresh Entry + its FTS mirror inside an existing tx.
func (s *Service) insertEntryInTx(ctx context.Context, qtx *sqlcdb.Queries, e *domain.Entry) error {
	if err := assignNewIDs(e); err != nil {
		return err
	}
	stampTimes(e, time.Now().UTC())
	e.IsCurrent = true
	params, err := entryToInsertParams(*e)
	if err != nil {
		return fmt.Errorf("repository: marshal entry: %w", err)
	}
	if err := qtx.InsertEntryRow(ctx, params); err != nil {
		return fmt.Errorf("repository: insert entry: %w", err)
	}
	if err := qtx.InsertEntryFTS(ctx, sqlcdb.InsertEntryFTSParams{
		EntryID: e.ID, Title: e.Title, Body: e.Body,
	}); err != nil {
		return fmt.Errorf("repository: fts insert: %w", err)
	}
	return nil
}

// supersedeEntryInTx is the legacy entry point (expectedVersion = 0) for
// callers that still use the original 3-arg supersede contract (notably
// materializeOne and SetGoalStatus). New callers should use
// supersedeEntryInTxWithExpected directly to participate in the
// optimistic-lock protocol.
func (s *Service) supersedeEntryInTx(ctx context.Context, qtx *sqlcdb.Queries, oldID string, replacement *domain.Entry) error {
	return s.supersedeEntryInTxWithExpected(ctx, qtx, oldID, 0, replacement)
}

// supersedeEntryInTxWithExpected flips an existing is_current=1 entry to
// superseded, removes its FTS row, and writes the replacement — all inside the
// supplied tx. Edges need no rewriting: they key on logical_id (decision (a)),
// which the replacement inherits.
//
// expectedVersion enforces optimistic locking when > 0: if the live row's
// Version does not match, the call fails with ErrVersionConflict and the
// tx must roll back. Pass 0 to skip (single-writer / legacy callers).
//
// Returns an error if oldID does not exist or is not the live version.
func (s *Service) supersedeEntryInTxWithExpected(ctx context.Context, qtx *sqlcdb.Queries, oldID string, expectedVersion int, replacement *domain.Entry) error {
	old, err := qtx.GetEntryByID(ctx, oldID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("repository: old entry %q not found: %w", oldID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("repository: load supersede target: %w", err)
	}
	if old.IsCurrent != 1 {
		return fmt.Errorf("repository: old entry %q is not current: %w", oldID, ErrNotFound)
	}
	if expectedVersion > 0 {
		liveVersion := int(nullInt64Or(old.Version, 1))
		if liveVersion != expectedVersion {
			return fmt.Errorf("%w: expected %d, got %d", ErrVersionConflict, expectedVersion, liveVersion)
		}
	}
	if err := assignNewIDs(replacement); err != nil {
		return err
	}
	replacement.LogicalID = old.LogicalID
	// Bitemporal supersede inheritance (red team #2 in grill doc):
	// the new version must keep the *original event time* unless the caller
	// supplied a fresh OccurredAt (e.g. status change at a known moment).
	// Without this, re-summarize / SetGoalStatus would drift occurred_at
	// to now() on every write — defeating the entire bitemporal model.
	if replacement.OccurredAt.IsZero() {
		if parsed, perr := parseRFCWithFallback(old.OccurredAt, old.CreatedAt); perr == nil {
			replacement.OccurredAt = parsed
		}
	}
	// Version always increments under supersede — the chain is monotonic.
	// We deliberately ignore replacement.Version here (it may be inherited
	// via struct copy from SetGoalStatus / similar code paths); the
	// authoritative source is old.Version + 1.
	replacement.Version = int(nullInt64Or(old.Version, 1)) + 1
	now := time.Now().UTC()
	stampTimes(replacement, now)
	replacement.IsCurrent = true

	nowRFC, ferr := formatRFC(now)
	if ferr != nil {
		return fmt.Errorf("repository: format supersede now: %w", ferr)
	}
	if err := qtx.FlipEntrySuperseded(ctx, sqlcdb.FlipEntrySupersededParams{
		SupersededBy: stringToNS(replacement.ID),
		UpdatedAt:    nowRFC,
		ID:           oldID,
	}); err != nil {
		return fmt.Errorf("repository: flip superseded: %w", err)
	}
	if err := qtx.DeleteEntryFTS(ctx, oldID); err != nil {
		return fmt.Errorf("repository: drop superseded fts: %w", err)
	}
	replacementParams, ferr := entryToInsertParams(*replacement)
	if ferr != nil {
		return fmt.Errorf("repository: marshal replacement: %w", ferr)
	}
	if err := qtx.InsertEntryRow(ctx, replacementParams); err != nil {
		return fmt.Errorf("repository: insert replacement: %w", err)
	}
	if err := qtx.InsertEntryFTS(ctx, sqlcdb.InsertEntryFTSParams{
		EntryID: replacement.ID, Title: replacement.Title, Body: replacement.Body,
	}); err != nil {
		return fmt.Errorf("repository: fts insert replacement: %w", err)
	}
	// No edge re-pointing: edges key on logical_id (decision (a), migration
	// 0017), and supersede preserves logical_id, so every edge touching this
	// entity stays valid automatically. The old rePointAllLiveEdges pass that
	// rewrote edges to the new version's id is gone.
	return nil
}

// pickActivityOccurredAt returns the best signal we have for the synthesised
// activity entry's event time, in order of trustworthiness:
//  1. earliest joined raw_commits.author_time across the change set
//  2. segments.occurred_at_min snapshot (UpsertSegment cached it)
//  3. zero (caller decides whether to fall back to now and set
//     QualityDegraded)
func pickActivityOccurredAt(earliestAuthor, segMin time.Time) time.Time {
	if !earliestAuthor.IsZero() {
		return earliestAuthor
	}
	if !segMin.IsZero() {
		return segMin
	}
	return time.Time{}
}

// listSegmentsNeedingMaterialize uses hand-written SQL because the optional
// repo filter does not slot cleanly into a sqlc query. Two specific queries
// would also work; this is simpler.
func (s *Service) listSegmentsNeedingMaterialize(ctx context.Context, scope MaterializeScope) ([]domain.Segment, error) {
	args := []any{string(domain.SummaryStatePending), string(domain.SummaryStateStale)}
	q := `SELECT id, repo_id, source, source_ref, summary_state, COALESCE(anchor_patch_id, ''),
                 metadata, COALESCE(ingested_at, created_at), updated_at,
                 COALESCE(occurred_at_min, ''), COALESCE(occurred_at_max, '')
            FROM segments
           WHERE summary_state IN (?, ?)`
	if scope.RepoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, scope.RepoID)
	}
	q += ` ORDER BY ingested_at ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list segments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Segment
	for rows.Next() {
		var seg domain.Segment
		var src, state, ingested, updated, occurredMin, occurredMax string
		if err := rows.Scan(
			&seg.ID, &seg.RepoID, &src, &seg.SourceRef, &state, &seg.AnchorPatchID,
			&seg.Metadata, &ingested, &updated, &occurredMin, &occurredMax,
		); err != nil {
			return nil, err
		}
		seg.Source = domain.Source(src)
		seg.SummaryState = domain.SummaryState(state)
		seg.IngestedAt, err = parseRFC(ingested)
		if err != nil {
			return nil, fmt.Errorf("repository: parse segment %s ingested_at: %w", seg.ID, err)
		}
		seg.UpdatedAt, err = parseRFC(updated)
		if err != nil {
			return nil, fmt.Errorf("repository: parse segment %s updated_at: %w", seg.ID, err)
		}
		if occurredMin != "" {
			seg.OccurredAtMin, err = parseRFC(occurredMin)
			if err != nil {
				return nil, fmt.Errorf("repository: parse segment %s occurred_at_min: %w", seg.ID, err)
			}
		}
		if occurredMax != "" {
			seg.OccurredAtMax, err = parseRFC(occurredMax)
			if err != nil {
				return nil, fmt.Errorf("repository: parse segment %s occurred_at_max: %w", seg.ID, err)
			}
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}
