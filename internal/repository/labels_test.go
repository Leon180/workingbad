package repository

import (
	"errors"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// seedActivity is a one-line helper for the label tests — every test
// here needs a starting entry and the existing helpers in service_test.go
// inline that boilerplate.
func seedActivity(t *testing.T, s *Service, title string) domain.Entry {
	t.Helper()
	e, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeActivity,
		Origin: domain.OriginLocal,
		Source: domain.SourceGit,
		Title:  title,
	})
	if err != nil {
		t.Fatalf("seedActivity: %v", err)
	}
	return e
}

func TestSetLabels_AddsAndReadsBack(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "PR title")

	if err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{
		domain.EntryTypeDecision,
		domain.EntryTypeResearch,
	}); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}

	got, err := s.GetLabels(ctx(t), e.ID)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	// GetLabels orders alphabetically (decision < research).
	want := []domain.EntryType{domain.EntryTypeDecision, domain.EntryTypeResearch}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestSetLabels_ReplaceIsIdempotent(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "PR title")

	if err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{
		domain.EntryTypeDecision, domain.EntryTypeResearch,
	}); err != nil {
		t.Fatalf("SetLabels 1: %v", err)
	}
	// Replace with a smaller set — should remove the dropped ones.
	if err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{
		domain.EntryTypeDecision,
	}); err != nil {
		t.Fatalf("SetLabels 2: %v", err)
	}
	got, _ := s.GetLabels(ctx(t), e.ID)
	if len(got) != 1 || got[0] != domain.EntryTypeDecision {
		t.Errorf("after replace, got %v want [decision]", got)
	}
}

func TestSetLabels_RejectsGoalAsLabel(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "PR title")

	err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{domain.EntryTypeGoal})
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for goal label, got %v", err)
	}
}

func TestSetLabels_RejectsLabelEqualToPrimaryType(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "PR title") // primary=activity

	err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{domain.EntryTypeActivity})
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for label=primary type, got %v", err)
	}
}

func TestSetLabels_RejectsDuplicateInInput(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "PR title")

	err := s.SetLabels(ctx(t), e.ID, []domain.EntryType{
		domain.EntryTypeDecision, domain.EntryTypeDecision,
	})
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for duplicate, got %v", err)
	}
}

func TestSetLabels_RejectsUnknownEntry(t *testing.T) {
	s := newService(t)
	err := s.SetLabels(ctx(t), "0192f6c0-7e31-7c2b-9b8a-ffffffffffff", []domain.EntryType{
		domain.EntryTypeDecision,
	})
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSetLabels_RejectsSupersededEntry(t *testing.T) {
	s := newService(t)
	e := seedActivity(t, s, "v1")

	// Supersede e so it stops being live.
	v2, err := s.Supersede(ctx(t), e.ID, e.Version, domain.Entry{
		Type:   domain.EntryTypeActivity,
		Origin: domain.OriginLocal,
		Source: domain.SourceGit,
		Title:  "v2",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	_ = v2 // we set labels on the now-superseded e, expect ErrNotFound

	err = s.SetLabels(ctx(t), e.ID, []domain.EntryType{domain.EntryTypeDecision})
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on superseded entry, got %v", err)
	}
}

func TestEntriesWithLabel_UnionsPrimaryAndSecondary(t *testing.T) {
	s := newService(t)
	// Three entries with different primary types
	act := seedActivity(t, s, "an activity")
	dec, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeDecision,
		Origin: domain.OriginLocal, Source: domain.SourceManual,
		SourceRef: "manual-decision-1",
		Title:     "a decision",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeResearch,
		Origin: domain.OriginLocal, Source: domain.SourceManual,
		SourceRef: "manual-research-1",
		Title:     "a research",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add `decision` as a SECONDARY label on the activity entry.
	if err := s.SetLabels(ctx(t), act.ID, []domain.EntryType{domain.EntryTypeDecision}); err != nil {
		t.Fatal(err)
	}

	// EntriesWithLabel(decision) should return both `dec` (primary) and `act` (secondary).
	got, err := s.EntriesWithLabel(ctx(t), domain.EntryTypeDecision, 100)
	if err != nil {
		t.Fatalf("EntriesWithLabel: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if !ids[dec.ID] {
		t.Errorf("missing primary decision entry %s", dec.ID)
	}
	if !ids[act.ID] {
		t.Errorf("missing secondary-labeled entry %s", act.ID)
	}
	if ids[res.ID] {
		t.Errorf("research entry leaked into decision result: %s", res.ID)
	}
}

func TestEntriesWithLabel_RejectsGoal(t *testing.T) {
	s := newService(t)
	_, err := s.EntriesWithLabel(ctx(t), domain.EntryTypeGoal, 10)
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for goal, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "activity, research, discuss, decision") {
		t.Errorf("error message missing closed-set hint: %v", err)
	}
}
