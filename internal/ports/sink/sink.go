// Package sink declares the Sink interface and the supersede-stable
// idempotency model that backs sync_state.
package sink

import (
	"context"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/ports/source"
)

// SubjectKind tags the stable identity Sinks key on for idempotency. Tying
// sync_state to (kind, ref) instead of entry_id avoids double-posting after
// supersede mints a new id.
type SubjectKind string

const (
	// SubjectKindGit uses the segment.source_ref (encode(repo_id, branch,
	// anchor_patch_id)) — stable across re-Summarize of the same segment.
	SubjectKindGit SubjectKind = "git"

	// SubjectKindEntry uses entry.logical_id — stable across edits/supersede.
	SubjectKindEntry SubjectKind = "entry"
)

// SyncItem is one batch element.
type SyncItem struct {
	SubjectKind SubjectKind
	SubjectRef  string
	EntryID     string // for sync_state.last_synced_entry_id
	Payload     []byte
	Hash        string // hash of Render(push_fields projection)
	ExternalRef string // empty on create; existing remote id on update
}

// SyncBatch groups items pushed in one Sink.Sync call.
type SyncBatch struct {
	Items []SyncItem
}

// Message is a Render output: opaque payload + content hash used for
// "same as last sync → skip".
type Message struct {
	Payload []byte
	Hash    string
}

// SyncPolicy selects and renders entries into messages. Each Sink supplies
// its own policy at registry wiring.
type SyncPolicy interface {
	Select(entries []domain.Entry) []domain.Entry
	Render(e domain.Entry) (Message, error)
}

// Sink pushes curated entries to an external product. `Sync` is
// upsert-by-external_ref (create / update / skip-by-hash), never append.
type Sink interface {
	Name() string
	Capability() source.Capability // shared shape; ∩=∅ enforced at wiring
	Sync(ctx context.Context, batch SyncBatch) error
}
