package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository/sqlcdb"
)

// Conversion between sqlc-generated structs (sql.NullString for nullable
// columns, int64 for booleans, RFC3339 strings for times) and our domain
// types (plain string / bool / time.Time, with empty string standing in
// for NULL). All boundary conversion lives here.
//
// Bitemporal mapping (grill: docs/grill/2026-06-08-time-travel-schema.md):
//   - sqlc.Entry.OccurredAt (NULL) → domain.Entry.OccurredAt; falls back to
//     IngestedAt then CreatedAt for rows ingested before migration 0009.
//   - sqlc.Entry.IngestedAt (NULL) → domain.Entry.IngestedAt; falls back to
//     legacy CreatedAt for the same reason.
//   - sqlc.Entry.CreatedAt (legacy NOT NULL) is still written by InsertEntry
//     for now so existing schema constraints hold; the v0.1.0-tag squash
//     migration will drop the column.

// ErrZeroTime is returned by formatRFC when given a zero time.Time. The
// previous behaviour silently substituted time.Now(); this was P0 silent
// corruption and is now refused at the boundary. Callers that legitimately
// want "now" must pass time.Now().UTC() explicitly.
var ErrZeroTime = errors.New("repository: refuse to format zero time as RFC3339Nano")

func entryFromSqlc(e sqlcdb.Entry) (domain.Entry, error) {
	ingested, err := parseRFCWithFallback(e.IngestedAt, e.CreatedAt)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("entry %s ingested_at: %w", e.ID, err)
	}
	occurred, err := parseRFCWithFallback(e.OccurredAt, e.CreatedAt)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("entry %s occurred_at: %w", e.ID, err)
	}
	updated, err := parseRFC(e.UpdatedAt)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("entry %s updated_at: %w", e.ID, err)
	}
	return domain.Entry{
		ID:              e.ID,
		LogicalID:       e.LogicalID,
		Type:            domain.EntryType(e.Type),
		Title:           e.Title,
		Body:            e.Body,
		Source:          domain.Source(e.Source),
		SourceRef:       nsToString(e.SourceRef),
		SourceEventHash: nsToString(e.SourceEventHash),
		Origin:          domain.Origin(e.Origin),
		RepoID:          nsToString(e.RepoID),
		Author:          nsToString(e.Author),
		Actor:           nsToString(e.Actor),
		Reason:          nsToString(e.Reason),
		Status:          domain.Status(nsToString(e.Status)),
		Version:         int(nullInt64Or(e.Version, 1)),
		IsCurrent:       e.IsCurrent == 1,
		SupersededBy:    nsToString(e.SupersededBy),
		Metadata:        e.Metadata,
		QualityDegraded: nullInt64Or(e.QualityDegraded, 0) == 1,
		OccurredAt:      occurred,
		IngestedAt:      ingested,
		UpdatedAt:       updated,
	}, nil
}

func edgeFromSqlc(e sqlcdb.Edge) (domain.Edge, error) {
	ingested, err := parseRFCWithFallback(e.IngestedAt, e.CreatedAt)
	if err != nil {
		return domain.Edge{}, fmt.Errorf("edge %s ingested_at: %w", e.ID, err)
	}
	occurred, err := parseRFCWithFallback(e.OccurredAt, e.CreatedAt)
	if err != nil {
		return domain.Edge{}, fmt.Errorf("edge %s occurred_at: %w", e.ID, err)
	}
	return domain.Edge{
		ID:           e.ID,
		FromID:       e.FromID,
		ToID:         e.ToID,
		Relation:     domain.Relation(e.Relation),
		Actor:        nsToString(e.Actor),
		Reason:       nsToString(e.Reason),
		IsCurrent:    e.IsCurrent == 1,
		SupersededBy: nsToString(e.SupersededBy),
		Metadata:     e.Metadata,
		Confidence:   e.Confidence,
		OccurredAt:   occurred,
		IngestedAt:   ingested,
	}, nil
}

func rawChangeFromSqlc(r sqlcdb.RawChange) (domain.RawChange, error) {
	ingested, err := parseRFCWithFallback(r.IngestedAt, r.CreatedAt)
	if err != nil {
		return domain.RawChange{}, fmt.Errorf("raw_change %s ingested_at: %w", r.ChangeID, err)
	}
	return domain.RawChange{
		ChangeID:   r.ChangeID,
		RepoID:     r.RepoID,
		PatchID:    nsToString(r.PatchID),
		IngestedAt: ingested,
	}, nil
}

func entryToInsertParams(e domain.Entry) (sqlcdb.InsertEntryRowParams, error) {
	metadata := e.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	if e.Version <= 0 {
		e.Version = 1
	}
	occurred, err := formatRFC(e.OccurredAt)
	if err != nil {
		return sqlcdb.InsertEntryRowParams{}, fmt.Errorf("entry %s occurred_at: %w", e.ID, err)
	}
	ingested, err := formatRFC(e.IngestedAt)
	if err != nil {
		return sqlcdb.InsertEntryRowParams{}, fmt.Errorf("entry %s ingested_at: %w", e.ID, err)
	}
	updated, err := formatRFC(e.UpdatedAt)
	if err != nil {
		return sqlcdb.InsertEntryRowParams{}, fmt.Errorf("entry %s updated_at: %w", e.ID, err)
	}
	qDegraded := int64(0)
	if e.QualityDegraded {
		qDegraded = 1
	}
	return sqlcdb.InsertEntryRowParams{
		ID:              e.ID,
		LogicalID:       e.LogicalID,
		Type:            string(e.Type),
		Title:           e.Title,
		Body:            e.Body,
		Source:          string(e.Source),
		SourceRef:       stringToNS(e.SourceRef),
		SourceEventHash: stringToNS(e.SourceEventHash),
		Origin:          string(e.Origin),
		RepoID:          stringToNS(e.RepoID),
		Author:          stringToNS(e.Author),
		Actor:           stringToNS(e.Actor),
		Reason:          stringToNS(e.Reason),
		Status:          stringToNS(string(e.Status)),
		IsCurrent:       boolToI64(e.IsCurrent),
		SupersededBy:    stringToNS(e.SupersededBy),
		Metadata:        metadata,
		Version:         sql.NullInt64{Int64: int64(e.Version), Valid: true},
		QualityDegraded: sql.NullInt64{Int64: qDegraded, Valid: true},
		OccurredAt:      stringToNS(occurred),
		IngestedAt:      stringToNS(ingested),
		// Legacy column kept in lockstep with ingested_at until the pre-v0.1.0
		// squash drops it. Sustaining both writes avoids a table rebuild on
		// modernc.org/sqlite (which would need PRAGMA dance + FK off).
		CreatedAt: ingested,
		UpdatedAt: updated,
	}, nil
}

// Small helpers.

func nsToString(n sql.NullString) string {
	if !n.Valid {
		return ""
	}
	return n.String
}

func stringToNS(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToI64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullInt64Or(n sql.NullInt64, fallback int64) int64 {
	if !n.Valid {
		return fallback
	}
	return n.Int64
}

// optTimeToNS marshals an optional time.Time to sql.NullString. Zero ⇒ NULL
// (no silent now() substitution). Callers populate the field only when they
// have a real signal; the DB column stays NULL otherwise.
func optTimeToNS(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}

// optNSToTime is the read-side counterpart to optTimeToNS. NULL → zero time.
func optNSToTime(n sql.NullString) (time.Time, error) {
	if !n.Valid || n.String == "" {
		return time.Time{}, nil
	}
	return parseRFC(n.String)
}

// earliestTimeFromAny extracts a time.Time from sqlc-typed `any` columns
// (computed subqueries like MIN(author_time) come back as interface{} from
// SQLite). Returns (zero, false) when the value is NULL / nil / unparseable.
func earliestTimeFromAny(v any) (time.Time, bool) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, false
	case string:
		if t == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	case []byte:
		if len(t) == 0 {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, string(t))
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	default:
		return time.Time{}, false
	}
}

// parseRFC parses an RFC3339Nano string into a time.Time. Empty strings are
// treated as a hard error: NOT NULL columns should never produce empty values
// downstream, and an empty value would round-trip to a zero time and then back
// to time.Now() under the old formatRFC behaviour — exactly the silent
// corruption pattern this fix exists to prevent.
func parseRFC(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("repository: empty RFC3339Nano string")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("repository: parse RFC3339Nano %q: %w", s, err)
	}
	return t, nil
}

// parseRFCWithFallback parses the primary nullable RFC3339Nano string; if
// it's NULL, it parses the legacy NOT NULL fallback. Used during the
// migration window where occurred_at/ingested_at may be NULL on rows that
// pre-date 0009 (post-migration backfill makes the fallback always equal
// to ingested_at).
func parseRFCWithFallback(primary sql.NullString, fallback string) (time.Time, error) {
	if primary.Valid && primary.String != "" {
		return parseRFC(primary.String)
	}
	return parseRFC(fallback)
}

// formatRFC formats a time.Time as RFC3339Nano in UTC. Zero times return
// ErrZeroTime — this is intentional. The previous fallback to time.Now() was
// P0 silent corruption: a caller forgetting to stamp a time would silently
// get the ingestion time written as if it were a real event timestamp. Callers
// that genuinely want server-now must pass time.Now().UTC() explicitly.
func formatRFC(t time.Time) (string, error) {
	if t.IsZero() {
		return "", ErrZeroTime
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
