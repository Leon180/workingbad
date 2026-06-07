// Package config defines the parsed shape of `config.yaml` and the loader.
//
// Boundary: all values that influence runtime are validated here; nothing
// downstream of LoadFile should re-validate the same fields.
package config

import "os"

// Secret is the *name* of an environment variable holding a secret value.
// We never store the plaintext secret on disk or in process state — only the
// pointer to where the OS keeps it.
//
// All standard stringification paths (fmt, YAML, JSON) intentionally redact
// to "[REDACTED]" so accidental logging never leaks the env var name either.
type Secret string

// String implements fmt.Stringer with a redacted constant. fmt's %s/%v
// formatting consults Stringer for named string types, so this catches the
// common accidental log path.
func (s Secret) String() string { return redacted }

// MarshalYAML implements yaml.Marshaler.
func (s Secret) MarshalYAML() (any, error) { return redacted, nil }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Resolve reads the named environment variable. Returns ("", false) if the
// Secret is empty or the env var is unset.
func (s Secret) Resolve() (string, bool) {
	if s == "" {
		return "", false
	}
	return os.LookupEnv(string(s))
}

const redacted = "[REDACTED]"
