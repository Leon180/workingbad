package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// CLI smoke tests: drive workingbad through a realistic dogfooding flow
// against a fresh SQLite in t.TempDir(). Verifies the binary's argument
// parsing + adapter wiring, not the repository service (which has its own
// table-driven tests in internal/repository/).

func TestCLI_NoteListAttachStatus(t *testing.T) {
	cfgPath, _ := setupRun(t)

	// 1. Create a goal.
	stdout := captureStdout(t, func() error {
		return runCLI(cfgPath, "goal", "Ship Phase 1 Slice A")
	})
	goalID := mustExtractCreatedID(t, stdout, "goal")

	// 2. Create a research note.
	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "note", "Investigating sqlc")
	})
	noteID := mustExtractCreatedID(t, stdout, "research")

	// 3. Attach note to goal.
	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "attach", noteID, goalID)
	})
	if !strings.Contains(stdout, "attached edge") {
		t.Errorf("expected attach success, got %q", stdout)
	}

	// 4. List should show both entries.
	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "list")
	})
	if !strings.Contains(stdout, "Ship Phase 1 Slice A") {
		t.Errorf("list missing goal: %q", stdout)
	}
	if !strings.Contains(stdout, "Investigating sqlc") {
		t.Errorf("list missing note: %q", stdout)
	}

	// 5. SetGoalStatus → goal is replaced; list should show the new live row.
	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "status", goalID, "in_progress")
	})
	if !strings.Contains(stdout, "in_progress") {
		t.Errorf("status output unexpected: %q", stdout)
	}

	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "list", "--type", "goal")
	})
	if !strings.Contains(stdout, "in_progress") {
		t.Errorf("list --type goal should reflect new status: %q", stdout)
	}
}

func TestCLI_PendingAndSummarize(t *testing.T) {
	cfgPath, _ := setupRun(t)

	// With no segments, pending = 0 and summarize is a no-op.
	stdout := captureStdout(t, func() error {
		return runCLI(cfgPath, "pending")
	})
	if !strings.Contains(stdout, "pending segments: 0") {
		t.Errorf("expected 0 pending, got %q", stdout)
	}

	stdout = captureStdout(t, func() error {
		return runCLI(cfgPath, "summarize")
	})
	if !strings.Contains(stdout, "materialized: 0") {
		t.Errorf("expected 0 materialized, got %q", stdout)
	}
}

func TestCLI_ListEmpty(t *testing.T) {
	cfgPath, _ := setupRun(t)
	stdout := captureStdout(t, func() error {
		return runCLI(cfgPath, "list")
	})
	if !strings.Contains(stdout, "no entries") {
		t.Errorf("empty list should say '(no entries)', got %q", stdout)
	}
}

func TestCLI_VersionWithoutConfig(t *testing.T) {
	// version is the only command that tolerates a missing config.
	stdout := captureStdout(t, func() error {
		return runCLI("/nonexistent/config.yaml", "version")
	})
	if !strings.Contains(stdout, "workingbad") {
		t.Errorf("version should print binary version, got %q", stdout)
	}
	if !strings.Contains(stdout, "config unavailable") {
		t.Errorf("expected explicit config-unavailable note, got %q", stdout)
	}
}

// --- helpers ---

// setupRun creates a temp config + db path and returns the config path. The
// app auto-migrates on first open, so callers can immediately issue
// data-bearing commands.
func setupRun(t *testing.T) (cfgPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "workingbad.db")
	cfgPath = filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf(`
db:
  path: %s
ai:
  kind: local
  local:
    endpoint: http://localhost:11434
    model: llama3.1
`, dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, dbPath
}

// runCLI builds a fresh cli.Command (we can't reuse main()'s app between
// tests because flags persist across runs) and invokes it with the given
// args. The first arg is always inserted as the binary name.
func runCLI(cfgPath string, args ...string) error {
	app := &cli.Command{
		Name:     "workingbad",
		Flags:    globalFlags(),
		Commands: allCommands(),
		Action:   actionInit,
	}
	argv := append([]string{"workingbad", "--config", cfgPath}, args...)
	return app.Run(context.Background(), argv)
}

// captureStdout redirects os.Stdout for the duration of fn and returns what
// was written. CLI output goes to stdout; the integration test asserts on it.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var b strings.Builder
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()

	if err := fn(); err != nil {
		_ = w.Close()
		os.Stdout = orig
		<-done
		t.Fatalf("CLI run: %v", err)
	}
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// mustExtractCreatedID parses the "✓ created <type> <id>" output line.
func mustExtractCreatedID(t *testing.T, stdout, wantType string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "✓ created ") {
			continue
		}
		// "✓ created <type> <id>"
		parts := strings.Fields(line)
		if len(parts) != 4 {
			continue
		}
		if parts[2] != wantType {
			continue
		}
		return parts[3]
	}
	t.Fatalf("could not find created %s id in: %q", wantType, stdout)
	return ""
}
