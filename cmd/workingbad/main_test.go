package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
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

// TestCLI_Journey_AttachThenReSummarizePreservesGoalView is the scripted
// dogfooding regression: a multi-step engineer flow that single-action
// tests can't catch. Specifically guards the P0 rePoint fix — if anyone
// in the future removes rePointAllLiveEdges from supersedeEntryInTx, this
// test fails immediately.
//
// Flow:
//  1. engineer creates a goal via CLI
//  2. engineer's git work is ingested + materialized (we drive this through
//     the service directly because there's no CLI for ingest yet, Phase 2)
//  3. engineer attaches the activity to the goal via CLI
//  4. the segment goes stale (new commits arrive) and is re-summarised via CLI
//  5. CORE ASSERTION: GetGoalActivities still finds the new activity
//  6. engineer marks the goal done via CLI
//  7. SECOND ASSERTION: the new (superseded) goal still sees the activity
func TestCLI_Journey_AttachThenReSummarizePreservesGoalView(t *testing.T) {
	cfgPath, dbPath := setupRun(t)
	ctx := context.Background()

	// Step 1: engineer creates goal via CLI.
	out := captureStdout(t, func() error {
		return runCLI(cfgPath, "goal", "Ship v0.1.0")
	})
	goalID := mustExtractCreatedID(t, out, "goal")

	// Step 2: simulate git ingest using the repository service directly.
	// (No CLI sub-command for git ingest yet — that's Phase 2.)
	seedSegmentForJourney(t, ctx, dbPath, "repo-1", "ref-journey", "patch-journey", "sha-journey")

	// Materialise via CLI to prove the CLI path works.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "summarize")
	})
	if !strings.Contains(out, "materialized: 1") {
		t.Fatalf("first summarize: %q", out)
	}
	activityID := getLiveActivityIDByRef(t, dbPath, "ref-journey")

	// Step 3: attach via CLI.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "attach", activityID, goalID)
	})
	if !strings.Contains(out, "attached edge") {
		t.Fatalf("attach: %q", out)
	}

	// Step 4: mark segment stale and re-summarise.
	markSegmentStale(t, dbPath, "ref-journey")
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "summarize")
	})
	if !strings.Contains(out, "materialized: 1") {
		t.Fatalf("re-summarize: %q", out)
	}

	// Step 5: CORE P0 assertion — goal still sees an attached activity.
	got := goalActivities(t, ctx, dbPath, goalID)
	if len(got) != 1 {
		t.Fatalf("P0 REGRESSION: after re-summarise the goal lost its activity. got %d activities, want 1", len(got))
	}
	if got[0].ID == activityID {
		t.Errorf("got the old activity %q back; expected the newly-materialised replacement", activityID)
	}

	// Step 6: status → done via CLI.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "status", goalID, "done")
	})
	if !strings.Contains(out, "status=done") {
		t.Errorf("status output: %q", out)
	}

	// Step 7: SECOND assertion — the new live goal (after supersede) still
	// sees the activity. Look it up via LogicalID chain.
	newGoalID := currentEntryByLogicalID(t, dbPath, goalID)
	got = goalActivities(t, ctx, dbPath, newGoalID)
	if len(got) != 1 {
		t.Errorf("after status change: new goal lost the activity (re-point regression on goal-supersede): got %d", len(got))
	}
}

// --- journey-test helpers (kept here so they're easy to read alongside the test) ---

func seedSegmentForJourney(t *testing.T, ctx context.Context, dbPath, repo, sourceRef, patchID, sha string) {
	t.Helper()
	svc, closeSvc := openServiceFor(t, dbPath)
	defer closeSvc()

	if _, err := svc.UpsertSegment(ctx, domain.Segment{
		RepoID:        repo,
		Source:        domain.SourceGit,
		SourceRef:     sourceRef,
		AnchorPatchID: patchID,
	}); err != nil {
		t.Fatalf("UpsertSegment: %v", err)
	}
	now := time.Now().UTC()
	ch, err := svc.UpsertRaw(ctx, domain.RawCommit{
		SHA: sha, RepoID: repo,
		Author: "alice", Committer: "alice",
		AuthorTime: now, CommitTime: now,
		Message: "journey commit",
	}, patchID)
	if err != nil {
		t.Fatalf("UpsertRaw: %v", err)
	}
	segID := getSegmentIDByRef(t, dbPath, sourceRef)
	if err := svc.LinkSegmentRaw(ctx, segID, ch.ChangeID); err != nil {
		t.Fatalf("LinkSegmentRaw: %v", err)
	}
}

func openServiceFor(t *testing.T, dbPath string) (*repository.Service, func()) {
	t.Helper()
	db, err := repository.Open(dbPath)
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	return repository.NewService(db), func() { _ = db.Close() }
}

func openSQLFor(t *testing.T, dbPath string) (*sql.DB, func()) {
	t.Helper()
	// We use a fresh sql connection so we don't hold the service's pool
	// while running CLI commands in the same goroutine.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}

func getSegmentIDByRef(t *testing.T, dbPath, sourceRef string) string {
	t.Helper()
	db, closer := openSQLFor(t, dbPath)
	defer closer()
	var id string
	if err := db.QueryRow(`SELECT id FROM segments WHERE source_ref = ?`, sourceRef).Scan(&id); err != nil {
		t.Fatalf("locate segment %q: %v", sourceRef, err)
	}
	return id
}

func getLiveActivityIDByRef(t *testing.T, dbPath, sourceRef string) string {
	t.Helper()
	db, closer := openSQLFor(t, dbPath)
	defer closer()
	var id string
	if err := db.QueryRow(
		`SELECT id FROM entries WHERE type='activity' AND source_ref=? AND is_current=1`,
		sourceRef,
	).Scan(&id); err != nil {
		t.Fatalf("locate activity %q: %v", sourceRef, err)
	}
	return id
}

func markSegmentStale(t *testing.T, dbPath, sourceRef string) {
	t.Helper()
	db, closer := openSQLFor(t, dbPath)
	defer closer()
	if _, err := db.Exec(`UPDATE segments SET summary_state='stale' WHERE source_ref = ?`, sourceRef); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
}

func currentEntryByLogicalID(t *testing.T, dbPath, oldID string) string {
	t.Helper()
	db, closer := openSQLFor(t, dbPath)
	defer closer()
	var id string
	if err := db.QueryRow(
		`SELECT e.id FROM entries e
		 WHERE e.is_current = 1
		   AND e.logical_id = (SELECT logical_id FROM entries WHERE id = ?)`,
		oldID,
	).Scan(&id); err != nil {
		t.Fatalf("find current by logical: %v", err)
	}
	return id
}

func goalActivities(t *testing.T, ctx context.Context, dbPath, goalID string) []domain.Entry {
	t.Helper()
	svc, closeSvc := openServiceFor(t, dbPath)
	defer closeSvc()
	got, err := svc.GetGoalActivities(ctx, goalID)
	if err != nil {
		t.Fatalf("GetGoalActivities: %v", err)
	}
	return got
}

// Silence unused-import warnings if some helpers aren't referenced yet.
var _ = mock.New

// TestCLI_Journey_TimeTravelReproducesPastState is the scripted dogfooding
// journey for the bitemporal Slice A.5 work:
//  1. engineer creates a goal via CLI at T0
//  2. captures T0_snapshot
//  3. supersedes the goal (status open → in_progress) at T1
//  4. supersedes again (in_progress → done) at T2
//  5. list --at T0_snapshot must reproduce the v1 goal (status=open)
//  6. list --at now must show the v3 goal (status=done)
//  7. history <logical-id> must enumerate 3 versions newest-first
//
// This is the "git log" / "git show <hash>:<file>" equivalent for the
// truth source — and the user-facing payoff of the bitemporal foundation.
func TestCLI_Journey_TimeTravelReproducesPastState(t *testing.T) {
	cfgPath, dbPath := setupRun(t)

	// T0: create goal.
	out := captureStdout(t, func() error {
		return runCLI(cfgPath, "goal", "Ship v0.1.0")
	})
	goalID := mustExtractCreatedID(t, out, "goal")
	snapshotT0 := time.Now().UTC()

	// Need separation so ingest timestamps differ predictably.
	time.Sleep(50 * time.Millisecond)

	// T1: bump status open → in_progress.
	if out := captureStdout(t, func() error {
		return runCLI(cfgPath, "status", goalID, "in_progress")
	}); !strings.Contains(out, "in_progress") {
		t.Fatalf("first status change: %q", out)
	}

	time.Sleep(50 * time.Millisecond)

	// T2: bump in_progress → done.
	// We have to look up the new id since each status flip mints one.
	currentID := currentEntryByLogicalID(t, dbPath, goalID)
	if out := captureStdout(t, func() error {
		return runCLI(cfgPath, "status", currentID, "done")
	}); !strings.Contains(out, "status=done") {
		t.Fatalf("second status change: %q", out)
	}

	// 5: at T0_snapshot, the original open goal should be the live version.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "list", "--at", snapshotT0.Format(time.RFC3339Nano), "--type", "goal")
	})
	if !strings.Contains(out, "Ship v0.1.0") {
		t.Errorf("list --at T0: missing original title — %q", out)
	}
	if !strings.Contains(out, "open") {
		t.Errorf("list --at T0: should show status=open (v1) — %q", out)
	}
	if strings.Contains(out, "done") || strings.Contains(out, "in_progress") {
		t.Errorf("list --at T0: should NOT see later status values — %q", out)
	}

	// 6: at now (no --at), the latest done goal should be live.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "list", "--type", "goal")
	})
	if !strings.Contains(out, "done") {
		t.Errorf("list (live): should see done status — %q", out)
	}

	// 7: history enumerates the 3-version chain.
	logicalID := logicalIDOf(t, dbPath, goalID)
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "history", logicalID)
	})
	if !strings.Contains(out, "v1") || !strings.Contains(out, "v2") || !strings.Contains(out, "v3") {
		t.Errorf("history: must list 3 versions — %q", out)
	}
	if !strings.Contains(out, "(current)") {
		t.Errorf("history: must mark a current version — %q", out)
	}
}

// logicalIDOf returns the logical_id of the row with the given id. Used by
// the time-travel journey to pivot from "goal v1's id" to "the chain
// identifier".
func logicalIDOf(t *testing.T, dbPath, id string) string {
	t.Helper()
	db, closer := openSQLFor(t, dbPath)
	defer closer()
	var lid string
	if err := db.QueryRow(`SELECT logical_id FROM entries WHERE id = ?`, id).Scan(&lid); err != nil {
		t.Fatalf("logical_id lookup: %v", err)
	}
	return lid
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

// TestCLI_NodeListAndShow drives the node CLI surface. Nodes have no CLI
// create command (manual ops are web-only; CLI is read-only parity), so we
// seed them directly through the service against the same temp DB.
func TestCLI_NodeListAndShow(t *testing.T) {
	cfgPath, dbPath := setupRun(t)
	ctx := context.Background()

	db, err := repository.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := repository.NewService(db)
	v1, err := svc.CreateNode(ctx, domain.Node{Type: domain.EntryTypeResearch, Title: "investigate vectors"})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	v2, err := svc.SupersedeNode(ctx, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeResearch, Title: "investigate vectors v2",
	})
	if err != nil {
		t.Fatalf("SupersedeNode: %v", err)
	}
	_ = db.Close() // release the handle before the CLI opens its own

	// node list → live version only.
	out := captureStdout(t, func() error { return runCLI(cfgPath, "node", "list") })
	if !strings.Contains(out, "investigate vectors v2") {
		t.Errorf("node list missing live node: %q", out)
	}

	// node show → full chain, current marked.
	out = captureStdout(t, func() error { return runCLI(cfgPath, "node", "show", v2.ID) })
	for _, want := range []string{"v2", "v1", "investigate vectors v2", "(current)"} {
		if !strings.Contains(out, want) {
			t.Errorf("node show missing %q: %q", want, out)
		}
	}

	// node list --q → FTS search.
	out = captureStdout(t, func() error { return runCLI(cfgPath, "node", "list", "--q", "vectors") })
	if !strings.Contains(out, "investigate vectors v2") {
		t.Errorf("node list --q missing match: %q", out)
	}

	// node show <unknown> → friendly message, no error.
	out = captureStdout(t, func() error {
		return runCLI(cfgPath, "node", "show", "0192f6c0-7e31-7c2b-9b8a-ffffffffffff")
	})
	if !strings.Contains(out, "no such node") {
		t.Errorf("node show unknown: %q", out)
	}
}

func TestCLI_NodeListEmpty(t *testing.T) {
	cfgPath, _ := setupRun(t)
	out := captureStdout(t, func() error { return runCLI(cfgPath, "node", "list") })
	if !strings.Contains(out, "no nodes") {
		t.Errorf("expected 'no nodes', got %q", out)
	}
}

// TestCLI_NodeListTimeTravel exercises the --at bitemporal branch: a node
// stamped in the past then superseded must show its historical version at a
// past --at and the live version without --at.
func TestCLI_NodeListTimeTravel(t *testing.T) {
	cfgPath, dbPath := setupRun(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	db, err := repository.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := repository.NewService(db)
	v1, err := svc.CreateNode(ctx, domain.Node{
		Type: domain.EntryTypeDecision, Title: "decision-alpha", OccurredAt: t0, IngestedAt: t0,
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if _, err := svc.SupersedeNode(ctx, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeDecision, Title: "decision-omega",
	}); err != nil {
		t.Fatalf("SupersedeNode: %v", err)
	}
	_ = db.Close()

	out := captureStdout(t, func() error { return runCLI(cfgPath, "node", "list") })
	if !strings.Contains(out, "decision-omega") || strings.Contains(out, "decision-alpha") {
		t.Errorf("live node list should show omega not alpha: %q", out)
	}
	out = captureStdout(t, func() error { return runCLI(cfgPath, "node", "list", "--at", "2026-01-02") })
	if !strings.Contains(out, "decision-alpha") || strings.Contains(out, "decision-omega") {
		t.Errorf("node list --at should show alpha not omega: %q", out)
	}
}
