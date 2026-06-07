// Package source declares the Source interface and shared types every Source
// implementation honors. Mocks and the real local-git source live in this
// package's subdirectories.
package source

import (
	"context"
	"errors"

	"github.com/Leon180/workingbad/internal/domain"
)

// Checkpoint is an opaque cursor each Source defines for itself (per-branch
// hash blob for git, REST cursor for API sources, slack ts, ...). The core
// only stores/retrieves it as bytes; correctness anchors live in the data
// (raw sha unique, patch-id chain), not here.
type Checkpoint []byte

// Capability declares the field sets a Source/Sink touches. Verified at
// registry wiring: `FetchFields ∩ PushFields = ∅` makes echo loops
// structurally impossible (truth-source-schema invariant 3).
type Capability struct {
	FetchFields []string
	PushFields  []string
}

// Source ingests external state into the truth source.
//
// `Pull` is at-least-once + idempotent: checkpoint advance and data writes
// happen in separate transactions, and crash-recovery converges to the right
// state (truth-source-schema source_checkpoint contract).
type Source interface {
	Name() string
	Capability() Capability

	// Pull reads new state since the supplied checkpoint, normalizes it to
	// Entries, and returns the next checkpoint to persist.
	Pull(ctx context.Context, cur Checkpoint) ([]domain.Entry, Checkpoint, error)

	// Watch is long-running. May return ErrUnsupported, in which case the
	// scheduler degrades to periodic Pull at a configured interval.
	Watch(ctx context.Context, out chan<- domain.Entry) error
}

// ErrUnsupported signals Watch is not implemented; callers degrade to Pull.
var ErrUnsupported = errors.New("source: Watch unsupported, degrade to Pull")
