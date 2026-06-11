package domain_test

import (
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestSourceRefForManual_DeterministicAcrossSurfaces locks the hash
// derivation: CLI and Web UI must produce the same source_ref for the
// same input or the create-dedupe check silently lets duplicates land.
//
// The golden hex is the sha256 of "research\x00ship slice b\x00body
// text", computed once and checked in. If you change the derivation,
// you're breaking every existing manual entry's dedup key — update the
// golden AND document the migration.
func TestSourceRefForManual_DeterministicAcrossSurfaces(t *testing.T) {
	got := domain.SourceRefForManual(domain.EntryTypeResearch, "ship slice b", "body text")
	const want = "40dd8c34505f59a0e461f359cdd670f961e2c24e91ab177c2a8b854b26385009"
	if got != want {
		t.Errorf("SourceRefForManual = %q, want %q", got, want)
	}
}

// TestSourceRefForManual_NULSeparatorPreventsCollisions proves the NUL
// byte between fields actually disambiguates inputs that would collide
// under a naïve concat — e.g. ("ab", "cd") vs ("abc", "d").
func TestSourceRefForManual_NULSeparatorPreventsCollisions(t *testing.T) {
	a := domain.SourceRefForManual(domain.EntryTypeResearch, "ab", "cd")
	b := domain.SourceRefForManual(domain.EntryTypeResearch, "abc", "d")
	if a == b {
		t.Errorf("colliding hashes for distinct inputs: %q", a)
	}
}

// TestSourceRefForManual_TypeChangesHash proves type is part of the
// hash so a research + a goal with identical title/body don't collapse
// to the same source_ref.
func TestSourceRefForManual_TypeChangesHash(t *testing.T) {
	a := domain.SourceRefForManual(domain.EntryTypeResearch, "title", "body")
	b := domain.SourceRefForManual(domain.EntryTypeGoal, "title", "body")
	if a == b {
		t.Errorf("type not factored into hash: %q", a)
	}
}
