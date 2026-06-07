package config

import "time"

// Config is the parsed top-level configuration.
//
// Sources/Sinks carry a per-type raw config blob; strongly-typed parsing and
// fetch/push capability disjoint-set verification happen at registry wiring
// time (see connector-interface skill, not in this validator).
type Config struct {
	DB       DB                `koanf:"db" validate:"required"`
	AI       AI                `koanf:"ai" validate:"required"`
	Defaults Defaults          `koanf:"defaults"`
	Sources  []ConnectorConfig `koanf:"sources" validate:"dive"`
	Sinks    []ConnectorConfig `koanf:"sinks"   validate:"dive"`
}

// DB configures the embedded SQLite database.
type DB struct {
	Path string `koanf:"path" validate:"required"`
}

// AI is a discriminated union — exactly one of Local/API is honored based on
// Kind. validator's required_if covers the mandatory side; extra side being
// non-nil is harmless because the runtime only looks at the active variant.
type AI struct {
	Kind  string   `koanf:"kind"  validate:"required,oneof=local api"`
	Local *AILocal `koanf:"local" validate:"required_if=Kind local"`
	API   *AIAPI   `koanf:"api"   validate:"required_if=Kind api"`
}

// AILocal — Ollama-style local provider.
type AILocal struct {
	Endpoint string `koanf:"endpoint" validate:"required,url"`
	Model    string `koanf:"model"    validate:"required"`
}

// AIAPI — Anthropic-style cloud provider. APIKeyEnv stores only the env-var
// name; resolve to plaintext via Secret.Resolve.
type AIAPI struct {
	Model     string `koanf:"model"        validate:"required"`
	APIKeyEnv Secret `koanf:"api_key_env"  validate:"required"`
}

// Defaults are the per-source/per-sink fallback parameters locked in
// `docs/ROADMAP.md` and `truth-source-schema`. All three are config-tunable
// without migration; this struct exists so users can override per environment.
type Defaults struct {
	// GapThreshold is the inactivity gap that closes one activity segment.
	// Locked default: 90 minutes (grill log#2/#10, dogfooding-tunable).
	GapThreshold time.Duration `koanf:"gap_threshold"`

	// MaxCommits is the hard cap on commits per segment (defense against
	// runaway long-lived branches blowing up Summarize tokens). Locked: 200.
	MaxCommits int `koanf:"max_commits" validate:"min=0"`

	// CoalesceWindow is the manual-edit folding window. Locked: 5 minutes.
	// Set to a value > MaxDuration to disable folding entirely.
	CoalesceWindow time.Duration `koanf:"coalesce_window"`
}

// ConnectorConfig is the wire-shape of one source/sink entry. The Config blob
// is parsed by each connector at registry wiring; this layer only enforces
// type + name presence.
type ConnectorConfig struct {
	Type   string         `koanf:"type" validate:"required"`
	Name   string         `koanf:"name" validate:"required"`
	Config map[string]any `koanf:"config"`
}

// Locked defaults from ROADMAP / truth-source-schema. Exported so tests and
// dogfooding tools share one source of truth.
const (
	DefaultGapThreshold   = 90 * time.Minute
	DefaultMaxCommits     = 200
	DefaultCoalesceWindow = 5 * time.Minute
)

// applyDefaults fills zero-value Defaults fields with the locked constants.
// Called by LoadFile after Unmarshal but before validation, so the required
// validator sees populated fields.
func (c *Config) applyDefaults() {
	if c.Defaults.GapThreshold == 0 {
		c.Defaults.GapThreshold = DefaultGapThreshold
	}
	if c.Defaults.MaxCommits == 0 {
		c.Defaults.MaxCommits = DefaultMaxCommits
	}
	if c.Defaults.CoalesceWindow == 0 {
		c.Defaults.CoalesceWindow = DefaultCoalesceWindow
	}
}
