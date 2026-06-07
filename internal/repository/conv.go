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

// ErrZeroTime is returned by formatRFC when given a zero time.Time. The
// previous behaviour silently substituted time.Now(); this was P0 silent
// corruption and is now refused at the boundary. Callers that legitimately
// want "now" must pass time.Now().UTC() explicitly.
var ErrZeroTime = errors.New("repository: refuse to format zero time as RFC3339Nano")

func entryFromSqlc(e sqlcdb.Entry) (domain.Entry, error) {
	created, err := parseRFC(e.CreatedAt)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("entry %s created_at: %w", e.ID, err)
	}
	updated, err := parseRFC(e.UpdatedAt)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("entry %s updated_at: %w", e.ID, err)
	}
	return domain.Entry{
		ID:           e.ID,
		LogicalID:    e.LogicalID,
		Type:         domain.EntryType(e.Type),
		Title:        e.Title,
		Body:         e.Body,
		Source:       domain.Source(e.Source),
		SourceRef:    nsToString(e.SourceRef),
		Origin:       domain.Origin(e.Origin),
		RepoID:       nsToString(e.RepoID),
		Author:       nsToString(e.Author),
		Status:       domain.Status(nsToString(e.Status)),
		IsCurrent:    e.IsCurrent == 1,
		SupersededBy: nsToString(e.SupersededBy),
		Metadata:     e.Metadata,
		CreatedAt:    created,
		UpdatedAt:    updated,
	}, nil
}

func edgeFromSqlc(e sqlcdb.Edge) (domain.Edge, error) {
	created, err := parseRFC(e.CreatedAt)
	if err != nil {
		return domain.Edge{}, fmt.Errorf("edge %s created_at: %w", e.ID, err)
	}
	return domain.Edge{
		ID:           e.ID,
		FromID:       e.FromID,
		ToID:         e.ToID,
		Relation:     domain.Relation(e.Relation),
		IsCurrent:    e.IsCurrent == 1,
		SupersededBy: nsToString(e.SupersededBy),
		Metadata:     e.Metadata,
		CreatedAt:    created,
	}, nil
}

func rawChangeFromSqlc(r sqlcdb.RawChange) (domain.RawChange, error) {
	created, err := parseRFC(r.CreatedAt)
	if err != nil {
		return domain.RawChange{}, fmt.Errorf("raw_change %s created_at: %w", r.ChangeID, err)
	}
	return domain.RawChange{
		ChangeID:  r.ChangeID,
		RepoID:    r.RepoID,
		PatchID:   nsToString(r.PatchID),
		CreatedAt: created,
	}, nil
}

func entryToInsertParams(e domain.Entry) (sqlcdb.InsertEntryRowParams, error) {
	metadata := e.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	created, err := formatRFC(e.CreatedAt)
	if err != nil {
		return sqlcdb.InsertEntryRowParams{}, fmt.Errorf("entry %s created_at: %w", e.ID, err)
	}
	updated, err := formatRFC(e.UpdatedAt)
	if err != nil {
		return sqlcdb.InsertEntryRowParams{}, fmt.Errorf("entry %s updated_at: %w", e.ID, err)
	}
	return sqlcdb.InsertEntryRowParams{
		ID:           e.ID,
		LogicalID:    e.LogicalID,
		Type:         string(e.Type),
		Title:        e.Title,
		Body:         e.Body,
		Source:       string(e.Source),
		SourceRef:    stringToNS(e.SourceRef),
		Origin:       string(e.Origin),
		RepoID:       stringToNS(e.RepoID),
		Author:       stringToNS(e.Author),
		Status:       stringToNS(string(e.Status)),
		IsCurrent:    boolToI64(e.IsCurrent),
		SupersededBy: stringToNS(e.SupersededBy),
		Metadata:     metadata,
		CreatedAt:    created,
		UpdatedAt:    updated,
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
