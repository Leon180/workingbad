package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Secret redaction guards: a regression here means we are one fmt.Println away
// from leaking. These three paths cover the realistic accidental-log surfaces.

func TestSecret_Redaction(t *testing.T) {
	const envName = "MY_API_KEY"
	s := Secret(envName)

	if got := s.String(); got != "[REDACTED]" {
		t.Errorf("String() = %q, want [REDACTED]", got)
	}
	// Testing the fmt.Stringer-via-%s path is the point — that's where real
	// accidental log leaks happen. staticcheck would suggest s.String() but
	// that defeats the regression guard.
	if got := fmt.Sprintf("%s", s); got != "[REDACTED]" { //nolint:staticcheck // intentional %s path
		t.Errorf("fmt %%s = %q, want [REDACTED]", got)
	}
	if got := fmt.Sprintf("%v", s); got != "[REDACTED]" {
		t.Errorf("fmt %%v = %q, want [REDACTED]", got)
	}
	if got := fmt.Sprintf("%+v", s); got != "[REDACTED]" {
		t.Errorf("fmt %%+v = %q, want [REDACTED]", got)
	}

	yamlOut, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(yamlOut), "[REDACTED]") || strings.Contains(string(yamlOut), envName) {
		t.Errorf("yaml output leaks or missing redaction: %q", yamlOut)
	}

	jsonOut, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(jsonOut) != `"[REDACTED]"` || strings.Contains(string(jsonOut), envName) {
		t.Errorf("json output leaks or missing redaction: %s", jsonOut)
	}
}

func TestSecret_RedactionInsideStruct(t *testing.T) {
	// Secret embedded in a parent struct still redacts when the parent is
	// marshalled. This is the realistic accidental-log case.
	type wrap struct {
		Key Secret `json:"key" yaml:"key"`
	}
	w := wrap{Key: Secret("MY_API_KEY")}

	j, _ := json.Marshal(w)
	if !strings.Contains(string(j), "[REDACTED]") || strings.Contains(string(j), "MY_API_KEY") {
		t.Errorf("json wrapper leaks: %s", j)
	}
	y, _ := yaml.Marshal(w)
	if !strings.Contains(string(y), "[REDACTED]") || strings.Contains(string(y), "MY_API_KEY") {
		t.Errorf("yaml wrapper leaks: %s", y)
	}
}

func TestSecret_Resolve(t *testing.T) {
	const env = "TEST_SECRET_42"
	t.Setenv(env, "actual-value")
	s := Secret(env)
	v, ok := s.Resolve()
	if !ok || v != "actual-value" {
		t.Errorf("Resolve() = %q, %v; want actual-value, true", v, ok)
	}
}

func TestSecret_Resolve_Empty(t *testing.T) {
	if _, ok := Secret("").Resolve(); ok {
		t.Error("empty Secret resolved unexpectedly")
	}
}

func TestSecret_Resolve_Missing(t *testing.T) {
	// Pick an env name very unlikely to be set.
	if _, ok := Secret("WORKINGBAD_DEFINITELY_NOT_SET_X9Z").Resolve(); ok {
		t.Error("missing env var resolved unexpectedly")
	}
}

// LoadFile happy path + locked defaults.

func TestLoadFile_Valid(t *testing.T) {
	path := writeTempConfig(t, validConfigYAML)
	c, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if c.DB.Path != "./test.db" {
		t.Errorf("DB.Path = %q", c.DB.Path)
	}
	if c.AI.Kind != "api" {
		t.Errorf("AI.Kind = %q", c.AI.Kind)
	}
	if c.AI.API == nil {
		t.Fatal("AI.API is nil")
	}
	if c.AI.API.Model != "claude-sonnet-4-7" {
		t.Errorf("AI.API.Model = %q", c.AI.API.Model)
	}
	if c.AI.API.APIKeyEnv != Secret("ANTHROPIC_API_KEY") {
		t.Errorf("APIKeyEnv = %q", c.AI.API.APIKeyEnv)
	}

	// Locked defaults applied when not set in YAML.
	if c.Defaults.GapThreshold != DefaultGapThreshold {
		t.Errorf("GapThreshold = %v, want %v", c.Defaults.GapThreshold, DefaultGapThreshold)
	}
	if c.Defaults.MaxCommits != DefaultMaxCommits {
		t.Errorf("MaxCommits = %d, want %d", c.Defaults.MaxCommits, DefaultMaxCommits)
	}
	if c.Defaults.CoalesceWindow != DefaultCoalesceWindow {
		t.Errorf("CoalesceWindow = %v, want %v", c.Defaults.CoalesceWindow, DefaultCoalesceWindow)
	}

	if len(c.Sources) != 1 || c.Sources[0].Type != "git" {
		t.Errorf("Sources = %+v", c.Sources)
	}
}

func TestLoadFile_DefaultsOverride(t *testing.T) {
	path := writeTempConfig(t, validConfigYAML+`
defaults:
  gap_threshold: 30m
  max_commits: 100
  coalesce_window: 1m
`)
	c, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if c.Defaults.GapThreshold != 30*time.Minute {
		t.Errorf("override GapThreshold = %v", c.Defaults.GapThreshold)
	}
	if c.Defaults.MaxCommits != 100 {
		t.Errorf("override MaxCommits = %d", c.Defaults.MaxCommits)
	}
	if c.Defaults.CoalesceWindow != time.Minute {
		t.Errorf("override CoalesceWindow = %v", c.Defaults.CoalesceWindow)
	}
}

func TestLoadFile_AIDiscriminated(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"local valid", aiLocalYAML, false},
		{"api valid", aiAPIYAML, false},
		{"missing kind", aiMissingKindYAML, true},
		{"invalid kind", aiInvalidKindYAML, true},
		{"kind=local without local block", aiLocalNoBlockYAML, true},
		{"kind=api without api block", aiAPINoBlockYAML, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			_, err := LoadFile(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadFile_MissingDB(t *testing.T) {
	yaml := `
ai:
  kind: local
  local:
    endpoint: http://localhost:11434
    model: llama3.1
`
	if _, err := LoadFile(writeTempConfig(t, yaml)); err == nil {
		t.Error("expected validation error for missing db.path")
	}
}

func TestLoadFile_MissingPath(t *testing.T) {
	_, err := LoadFile("/definitely/does/not/exist.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// fixtures + helpers

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

const validConfigYAML = `
db:
  path: ./test.db
ai:
  kind: api
  api:
    model: claude-sonnet-4-7
    api_key_env: ANTHROPIC_API_KEY
sources:
  - type: git
    name: my-repo
    config:
      path: /tmp/repo
sinks: []
`

const aiLocalYAML = `
db:
  path: ./test.db
ai:
  kind: local
  local:
    endpoint: http://localhost:11434
    model: llama3.1
`

const aiAPIYAML = `
db:
  path: ./test.db
ai:
  kind: api
  api:
    model: claude-sonnet-4-7
    api_key_env: ANTHROPIC_API_KEY
`

const aiMissingKindYAML = `
db:
  path: ./test.db
ai:
  api:
    model: x
    api_key_env: K
`

const aiInvalidKindYAML = `
db:
  path: ./test.db
ai:
  kind: gemini
  api:
    model: x
    api_key_env: K
`

const aiLocalNoBlockYAML = `
db:
  path: ./test.db
ai:
  kind: local
`

const aiAPINoBlockYAML = `
db:
  path: ./test.db
ai:
  kind: api
`
