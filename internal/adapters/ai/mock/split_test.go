package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

func TestSplit_DefaultIsIdentity(t *testing.T) {
	p := mock.New()
	e := domain.Entry{Type: domain.EntryTypeActivity, Title: "fix the loop", Body: "details"}

	drafts, err := p.Split(context.Background(), e)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("default split → %d drafts, want 1", len(drafts))
	}
	d := drafts[0]
	if d.Type != e.Type || d.Title != e.Title || d.Body != e.Body {
		t.Fatalf("draft = %+v, want type/title/body mirrored from entry", d)
	}
}

func TestSplit_CountsTracked(t *testing.T) {
	p := mock.New()
	e := domain.Entry{Type: domain.EntryTypeActivity, Title: "t"}

	for i := 0; i < 3; i++ {
		if _, err := p.Split(context.Background(), e); err != nil {
			t.Fatalf("Split %d: %v", i, err)
		}
	}
	if _, _, _, split := p.Counts(); split != 3 {
		t.Fatalf("split calls = %d, want 3", split)
	}
}

func TestSplit_WithSplitFuncOverride(t *testing.T) {
	want := []domain.NodeDraft{
		{Type: domain.EntryTypeResearch, Title: "a"},
		{Type: domain.EntryTypeDecision, Title: "b"},
	}
	p := mock.New().WithSplitFunc(func(_ context.Context, _ domain.Entry) ([]domain.NodeDraft, error) {
		return want, nil
	})

	got, err := p.Split(context.Background(), domain.Entry{})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d drafts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("draft %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSplit_OverrideCanReturnError(t *testing.T) {
	sentinel := errors.New("boom")
	p := mock.New().WithSplitFunc(func(_ context.Context, _ domain.Entry) ([]domain.NodeDraft, error) {
		return nil, sentinel
	})

	if _, err := p.Split(context.Background(), domain.Entry{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}
