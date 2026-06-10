package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// SourceRefForManual builds the deterministic source_ref used by manual
// entry creation (CLI + Web UI). Same (type, title, body) input → same
// hash → the repository's create-dedupe check collapses re-submissions
// of identical content.
//
// Format: hex(sha256(type \x00 title \x00 body))
//
// The NUL byte separator prevents collisions like
// ("title=abc", "body=de") vs ("title=ab", "body=cde"). Don't reorder
// the inputs without bumping a version prefix — a silent change would
// orphan every existing manual entry from its dedup key.
func SourceRefForManual(typ EntryType, title, body string) string {
	h := sha256.New()
	h.Write([]byte(string(typ)))
	h.Write([]byte{0})
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}
