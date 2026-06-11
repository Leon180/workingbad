package repository

import (
	"errors"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestInsertEntry_NonGoalAcceptsArchived — issue #51: validator allows
// status=archived on activity/research/discuss/decision. Soft-delete is
// the use case: a duplicate or junk entry gets archived without
// supersede-with-empty-content gymnastics.
func TestInsertEntry_NonGoalAcceptsArchived(t *testing.T) {
	s := newService(t)
	for _, typ := range []domain.EntryType{
		domain.EntryTypeActivity,
		domain.EntryTypeResearch,
		domain.EntryTypeDiscuss,
		domain.EntryTypeDecision,
	} {
		t.Run(string(typ), func(t *testing.T) {
			e, err := s.InsertEntry(ctx(t), domain.Entry{
				Type:   typ,
				Origin: domain.OriginLocal,
				Source: domain.SourceManual, SourceRef: "ref-" + string(typ),
				Title:  "archived " + string(typ),
				Status: domain.StatusArchived,
			})
			if err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if e.Status != domain.StatusArchived {
				t.Errorf("status round-trip = %q, want archived", e.Status)
			}
		})
	}
}

// TestInsertEntry_NonGoalRejectsOtherStatuses — only archived is allowed.
// open / in_progress / done remain goal-only.
func TestInsertEntry_NonGoalRejectsOtherStatuses(t *testing.T) {
	s := newService(t)
	for _, st := range []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusDone,
	} {
		t.Run(string(st), func(t *testing.T) {
			_, err := s.InsertEntry(ctx(t), domain.Entry{
				Type:   domain.EntryTypeActivity,
				Origin: domain.OriginLocal,
				Source: domain.SourceGit,
				Title:  "x",
				Status: st,
			})
			if err == nil || !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput for status=%s, got %v", st, err)
			}
			if err != nil && !strings.Contains(err.Error(), "can only carry status=archived") {
				t.Errorf("error message lost the hint: %v", err)
			}
		})
	}
}

// TestListEntries_HidesArchivedByDefault — IncludeArchived=false (the
// zero-value default) drops archived entries from the result.
func TestListEntries_HidesArchivedByDefault(t *testing.T) {
	s := newService(t)
	// Active research
	active, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "active",
		Title: "still relevant",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Archived junk
	archived, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "junk",
		Title:  "junk",
		Status: domain.StatusArchived,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ListEntries(ctx(t), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	if !gotIDs[active.ID] {
		t.Errorf("active entry missing from default list")
	}
	if gotIDs[archived.ID] {
		t.Errorf("archived entry leaked into default list")
	}
}

// TestListEntries_IncludeArchivedShowsAll — flipping the flag surfaces
// both kinds.
func TestListEntries_IncludeArchivedShowsAll(t *testing.T) {
	s := newService(t)
	active, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "active2",
		Title: "keep",
	})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "junk2",
		Title:  "junk",
		Status: domain.StatusArchived,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ListEntries(ctx(t), ListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	if !gotIDs[active.ID] || !gotIDs[archived.ID] {
		t.Errorf("expected both entries in IncludeArchived list; active=%v archived=%v",
			gotIDs[active.ID], gotIDs[archived.ID])
	}
}
