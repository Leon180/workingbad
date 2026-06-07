// Package migrations holds the embedded SQL migration files plus the
// forward-only apply path. v0.1.0 will be the freeze point; before that tag,
// earlier migration files may be edited or squashed (see ROADMAP).
package migrations

import "embed"

// FS embeds every numbered SQL migration in this directory. goose orders
// them lexicographically by leading number.
//
//go:embed *.sql
var FS embed.FS
