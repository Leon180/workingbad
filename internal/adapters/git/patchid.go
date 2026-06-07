// Package git holds the local-git Source (Phase 1 Slice A) and the patch-id
// utility used to link rewrite history (amend / rebase / squash) across SHA
// boundaries. See truth-source-schema "raw 層" and connector-interface
// "local-git source" for the surrounding contract.
package git

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// PatchID computes a stable content fingerprint for a unified diff. Returns
// ("", false) when the input lacks a "diff --git" header (empty, merge with no
// payload, malformed).
//
// Semantics (the property we actually require):
//   - same logical change across amend / rebase / reorder → same patch-id
//   - different content (added/removed/changed lines) → different patch-id
//   - merge commits with no resolvable patch → ("", false), never used as a
//     segment anchor (truth-source-schema "raw 層")
//
// This is NOT bit-identical to `git patch-id --stable`. We strip the same
// volatile noise but use SHA-1 over our normalized buffer instead of mirroring
// git's per-file accumulation. Cross-tool interop is explicitly out of scope;
// we only need internal consistency for rewrite-chain linking.
//
// Normalisation:
//   - Drop everything before the first "diff --git" line.
//   - Drop "index ...", "new file mode ...", "deleted file mode ...",
//     "old mode ...", "new mode ..." — these vary across SHAs without a
//     logical change.
//   - Reduce hunk headers "@@ -a,b +c,d @@ ctx" to a sentinel "@@" — line
//     numbers shift on rebase but the hunk content is what we care about.
//   - Replace "Binary files X and Y differ" with a "BIN" sentinel so binary
//     changes still produce an id but do not collide with text patches.
//   - Trim trailing whitespace on every retained line.
func PatchID(diff []byte) (string, bool) {
	if len(bytes.TrimSpace(diff)) == 0 {
		return "", false
	}

	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(diff))
	// Allow large hunks; some refactor commits emit megabytes.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	started := false
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t\r")
		if !started {
			if !strings.HasPrefix(line, "diff --git") {
				continue
			}
			started = true
		}

		switch {
		case strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode "),
			strings.HasPrefix(line, "deleted file mode "),
			strings.HasPrefix(line, "old mode "),
			strings.HasPrefix(line, "new mode "):
			continue
		case strings.HasPrefix(line, "@@"):
			buf.WriteString("@@\n")
			continue
		case strings.HasPrefix(line, "Binary files "):
			buf.WriteString("BIN\n")
			continue
		}

		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", false
	}
	if !started || buf.Len() == 0 {
		return "", false
	}

	sum := sha1.Sum(buf.Bytes())
	return hex.EncodeToString(sum[:]), true
}
