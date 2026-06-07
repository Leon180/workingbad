package repository

import (
	"database/sql"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository/sqlcdb"
)

// Conversion between sqlc-generated structs (sql.NullString for nullable
// columns, int64 for booleans, RFC3339 strings for times) and our domain
// types (plain string / bool / time.Time, with empty string standing in
// for NULL). All boundary conversion lives here.

func entryFromSqlc(e sqlcdb.Entry) domain.Entry {
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
		CreatedAt:    parseRFC(e.CreatedAt),
		UpdatedAt:    parseRFC(e.UpdatedAt),
	}
}

func edgeFromSqlc(e sqlcdb.Edge) domain.Edge {
	return domain.Edge{
		ID:           e.ID,
		FromID:       e.FromID,
		ToID:         e.ToID,
		Relation:     domain.Relation(e.Relation),
		IsCurrent:    e.IsCurrent == 1,
		SupersededBy: nsToString(e.SupersededBy),
		Metadata:     e.Metadata,
		CreatedAt:    parseRFC(e.CreatedAt),
	}
}

func rawChangeFromSqlc(r sqlcdb.RawChange) domain.RawChange {
	return domain.RawChange{
		ChangeID:  r.ChangeID,
		RepoID:    r.RepoID,
		PatchID:   nsToString(r.PatchID),
		CreatedAt: parseRFC(r.CreatedAt),
	}
}

func entryToInsertParams(e domain.Entry) sqlcdb.InsertEntryRowParams {
	metadata := e.Metadata
	if metadata == "" {
		metadata = "{}"
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
		CreatedAt:    formatRFC(e.CreatedAt),
		UpdatedAt:    formatRFC(e.UpdatedAt),
	}
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

func parseRFC(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func formatRFC(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return t.UTC().Format(time.RFC3339Nano)
}
