package repository

import (
	"errors"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

func TestEntryBySourceRef_ResolvesToLiveAcrossSupersede(t *testing.T) {
	s := newService(t)

	// v1: GitHub-fetched issue
	v1, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeGoal,
		Origin: domain.OriginFetched, Source: domain.SourceGitHub,
		SourceRef: "issue-12",
		Title:     "perf collectAttached",
		Status:    domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("insert v1: %v", err)
	}

	// v2: same source_ref carried forward through supersede (status change)
	v2, err := s.Supersede(ctx(t), v1.ID, v1.Version, domain.Entry{
		Type:   domain.EntryTypeGoal,
		Origin: domain.OriginFetched, Source: domain.SourceGitHub,
		SourceRef: "issue-12",
		Title:     "perf collectAttached",
		Status:    domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("supersede v2: %v", err)
	}

	// EntryBySourceRef should return v2 (the live version), NOT v1 (the
	// anchor in entry_source_refs). The chain walk happens automatically.
	got, err := s.EntryBySourceRef(ctx(t), domain.SourceGitHub, "issue-12")
	if err != nil {
		t.Fatalf("EntryBySourceRef: %v", err)
	}
	if got.ID != v2.ID {
		t.Errorf("got id %s, want live v2.ID %s", got.ID, v2.ID)
	}
	if got.Status != domain.StatusDone {
		t.Errorf("got status %s, want done", got.Status)
	}
}

func TestEntryBySourceRef_NotFound(t *testing.T) {
	s := newService(t)
	_, err := s.EntryBySourceRef(ctx(t), domain.SourceGitHub, "issue-9999")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestEntryBySourceRef_RejectsEmpty(t *testing.T) {
	s := newService(t)
	_, err := s.EntryBySourceRef(ctx(t), "", "")
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAllSourceRefs_OnLiveAnchor(t *testing.T) {
	s := newService(t)
	v1, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeActivity,
		Origin: domain.OriginFetched, Source: domain.SourceGitHub,
		SourceRef: "pr-42",
		Title:     "perf PR",
	})
	if err != nil {
		t.Fatal(err)
	}

	aliases, err := s.AllSourceRefs(ctx(t), v1.ID)
	if err != nil {
		t.Fatalf("AllSourceRefs: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("got %d aliases, want 1: %v", len(aliases), aliases)
	}
	if aliases[0].Source != domain.SourceGitHub || aliases[0].SourceRef != "pr-42" {
		t.Errorf("got %+v, want {github, pr-42}", aliases[0])
	}
}

// TestAllSourceRefs_UnknownEntryReturnsNotFound guards the
// "ErrNotFound vs empty result" contract after the 2-query → self-join
// refactor (the single-query path needs an explicit existence check to
// distinguish unknown entry from entry-with-no-aliases).
func TestAllSourceRefs_UnknownEntryReturnsNotFound(t *testing.T) {
	s := newService(t)
	_, err := s.AllSourceRefs(ctx(t), "0192f6c0-7e31-7c2b-9b8a-ffffffffffff")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for unknown id, got %v", err)
	}
}

// TestInsertEntry_NoSourceRef_OmitsAlias guards the "empty SourceRef
// skips alias write" branch in insertSourceRefAliasTx — git activities
// without a deterministic ref should not create alias rows.
func TestInsertEntry_NoSourceRef_OmitsAlias(t *testing.T) {
	s := newService(t)
	e, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeActivity,
		Origin: domain.OriginLocal, Source: domain.SourceGit,
		Title: "no-ref activity",
	})
	if err != nil {
		t.Fatal(err)
	}
	aliases, err := s.AllSourceRefs(ctx(t), e.ID)
	if err != nil {
		t.Fatalf("AllSourceRefs: %v", err)
	}
	if len(aliases) != 0 {
		t.Errorf("got %d aliases, want 0: %v", len(aliases), aliases)
	}
}
