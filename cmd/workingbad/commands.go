package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/migrations"
	"github.com/Leon180/workingbad/internal/repository"
)

// allCommands returns the public CLI surface.
func allCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name:  "migrate",
			Usage: "run pending database migrations and print the resulting version",
			Description: "Applies any forward-only migrations bundled into the binary and exits.\n" +
				"Migrations also run automatically on every command that opens the DB,\n" +
				"so this is mainly useful for inspecting the post-upgrade schema version.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad migrate",
			Action: actionMigrate,
		},
		{
			Name:  "version",
			Usage: "print the binary version and current migration version",
			Description: "Prints the workingbad binary version. If the configured database is\n" +
				"reachable it also prints the migration version it has been brought up to.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad version",
			Action: actionVersion,
		},
		{
			Name:  "serve",
			Usage: "start the localhost Web UI on 127.0.0.1 (blocks until Ctrl-C)",
			Description: "Launches the embedded net/http server bound to 127.0.0.1 only. The UI\n" +
				"shares the same RepositoryService as the CLI, so anything you create here\n" +
				"is visible there immediately. Press Ctrl-C to drain and stop.\n\n" +
				"EXAMPLES:\n" +
				"  workingbad serve\n" +
				"  workingbad serve --port 7890\n" +
				"  workingbad --config ~/.workingbad/config.yaml serve",
			Flags: []cli.Flag{
				&cli.IntFlag{Name: "port", Usage: "listen port (overrides web.port from config.yaml; 0 = use config)"},
			},
			Action: actionServe,
		},

		// Phase 1 dogfooding surface.
		{
			Name:      "note",
			Usage:     "record a free-form research note (entry of type=research)",
			ArgsUsage: "<title> [body words...]",
			Description: "Creates a research entry (origin=local, source=manual). Useful for\n" +
				"capturing investigations, links, half-baked ideas — anything that is not\n" +
				"yet a decision or a goal. The body is everything after the title, joined\n" +
				"by single spaces; quote multi-word phrases to preserve them.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad note \"sqlite WAL\" \"WAL gives concurrent readers + 1 writer\"",
			Action: actionNote,
		},
		{
			Name:      "decision",
			Usage:     "record a decision you have made (entry of type=decision)",
			ArgsUsage: "<title> [body words...]",
			Description: "Creates a decision entry — the dogfood unit for architecture / product\n" +
				"calls. Pair with `attach` to link the decision under the goal it serves.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad decision \"use modernc/sqlite\" \"pure Go, no cgo, FTS5 built-in\"",
			Action: actionDecision,
		},
		{
			Name:      "goal",
			Usage:     "create a new goal (status starts at 'open'; use `status` to advance)",
			ArgsUsage: "<title> [body words...]",
			Description: "A goal is the aggregation root: activities, notes and decisions get\n" +
				"attached to it via `attach`. Status defaults to open; advance it later\n" +
				"with `workingbad status <goal-id> <new-status>`.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad goal \"ship Slice B\" \"web UI + bitemporal time-travel\"",
			Action: actionGoal,
		},
		{
			Name:  "list",
			Usage: "list live entries (or, with --at, the state at a past timestamp)",
			Description: "Without --at: prints every is_current=1 entry, newest first. With --at:\n" +
				"prints the bitemporal snapshot — what was live at that wall-clock moment,\n" +
				"using ingested_at to roll back the supersede chain.\n\n" +
				"EXAMPLES:\n" +
				"  workingbad list\n" +
				"  workingbad list --type goal\n" +
				"  workingbad list --type decision --limit 50\n" +
				"  workingbad list --at 2026-06-08T14:00:00Z\n" +
				"  workingbad list --at 2026-06-08            # date = midnight UTC",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "type", Usage: "show only entries of this type (activity|research|discuss|decision|goal)"},
				&cli.StringFlag{Name: "repo", Usage: "show only entries from this repo_id (isolation key; empty = all repos)"},
				&cli.IntFlag{Name: "limit", Value: 20, Usage: "stop after printing this many rows"},
				&cli.StringFlag{Name: "at", Usage: "time-travel snapshot at RFC3339 timestamp or YYYY-MM-DD (midnight UTC)"},
			},
			Action: actionList,
		},
		{
			Name:      "history",
			Usage:     "print every version of a logical entry, oldest to newest (bitemporal git log)",
			ArgsUsage: "<logical-id>",
			Description: "Walks the supersede chain for a logical_id and prints each version with\n" +
				"its occurred_at (when the event happened) next to its ingested_at (when\n" +
				"we recorded it). The current version is marked.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad history 0192f6c0-7e31-7c2b-9b8a-1b2c3d4e5f60",
			Action: actionHistory,
		},
		{
			Name:      "attach",
			Usage:     "link an entry under a goal (creates a part_of edge)",
			ArgsUsage: "<entry-id> <goal-id>",
			Description: "Makes <entry-id> show up inside <goal-id>'s aggregation. Both arguments\n" +
				"are entry IDs (not logical_ids). Use `list` to find them.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad attach <note-id> <goal-id>",
			Action: actionAttach,
		},
		{
			Name:      "detach",
			Usage:     "mark a part_of edge as detached (append-only; original row is preserved)",
			ArgsUsage: "<edge-id>",
			Description: "Edges are append-only, so detach does not delete — it supersedes the\n" +
				"edge with a detached version. The entry stops appearing under the goal\n" +
				"but the history is retained for time-travel.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad detach <edge-id>",
			Action: actionDetach,
		},
		{
			Name:      "status",
			Usage:     "advance a goal's status (creates a new supersede version)",
			ArgsUsage: "<goal-id> <open|in_progress|done|archived>",
			Description: "Status transitions are immutable: a new entry version is created and\n" +
				"the old one is marked superseded. Prints the new entry ID so you can\n" +
				"chain commands.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad status <goal-id> in_progress",
			Action: actionStatus,
		},
		{
			Name:  "pending",
			Usage: "count git segments that have not yet been summarised into activity entries",
			Description: "A segment is a window of related git commits. `summarize` turns each\n" +
				"pending segment into one activity entry via the AIProvider. This command\n" +
				"just reports the backlog size; it does not mutate state.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad pending",
			Action: actionPending,
		},
		{
			Name:  "summarize",
			Usage: "materialise every pending segment into an activity entry (Phase 1: mock AI)",
			Description: "Runs BatchMaterialize over all pending segments, one independent tx\n" +
				"per segment. Phase 1 ships the deterministic mock AIProvider; the real\n" +
				"local (Ollama) / api (Claude) providers arrive in Phase 3 — see ROADMAP.\n\n" +
				"EXAMPLE:\n" +
				"  workingbad summarize",
			Action: actionSummarize,
		},
		{
			Name:  "seed-github",
			Usage: "fetch issues + PRs from a GitHub repo and seed them as truth-source entries (read-only)",
			Description: "Phase 1 dogfood seeder. Issues become goal entries, PRs become activity\n" +
				"entries, and \"Closes/Fixes/Resolves #N\" references in PR bodies become\n" +
				"part_of edges from the PR into the matching goal. Uses `gh auth token`\n" +
				"if GH_TOKEN env is unset. Read-only against GitHub; never pushes.\n\n" +
				"EXAMPLES:\n" +
				"  workingbad seed-github --wipe                       # current repo, wipe local DB first\n" +
				"  workingbad seed-github --repo OtherOrg/otherrepo",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "repo", Usage: "owner/name (defaults to git remote 'origin' inferred from cwd)"},
				&cli.BoolFlag{Name: "wipe", Usage: "delete the configured SQLite file before seeding"},
			},
			Action: actionSeedGitHub,
		},
	}
}

// withService is the boilerplate-killer: load config, open DB, build a
// repository service, invoke the caller, close on the way out.
func withService(c *cli.Command, fn func(context.Context, *repository.Service) error) error {
	cfg, err := config.LoadFile(c.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	defer func() { _ = db.Close() }()
	svc := repository.NewService(db)
	return fn(context.Background(), svc)
}

// actionInit is the Phase 0 acceptance path: load config, open DB (which
// auto-migrates), report state, exit clean.
func actionInit(_ context.Context, c *cli.Command) error {
	return withService(c, func(ctx context.Context, _ *repository.Service) error {
		cfg, err := config.LoadFile(c.String("config"))
		if err != nil {
			return err
		}
		db, err := repository.Open(cfg.DB.Path)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		version, err := migrations.Version(db)
		if err != nil {
			return err
		}
		fmt.Printf(
			"workingbad %s\n  config = %s\n  db     = %s\n  migration_version = %d\n  ai_kind = %s\n",
			binaryVersion, c.String("config"), cfg.DB.Path, version, cfg.AI.Kind,
		)
		return nil
	})
}

func actionMigrate(_ context.Context, c *cli.Command) error {
	cfg, err := config.LoadFile(c.String("config"))
	if err != nil {
		return err
	}
	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	v, err := migrations.Version(db)
	if err != nil {
		return err
	}
	fmt.Printf("migrations: version=%d\n", v)
	return nil
}

func actionVersion(_ context.Context, c *cli.Command) error {
	cfg, err := config.LoadFile(c.String("config"))
	if err != nil {
		fmt.Printf("workingbad %s (config unavailable: %v)\n", binaryVersion, err)
		return nil
	}
	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		fmt.Printf("workingbad %s (db unavailable: %v)\n", binaryVersion, err)
		return nil
	}
	defer func() { _ = db.Close() }()
	v, err := migrations.Version(db)
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}
	fmt.Printf("workingbad %s (migration version %d)\n", binaryVersion, v)
	return nil
}

// --- entry-create commands (manual source) ---

func actionNote(ctx context.Context, c *cli.Command) error {
	title, body, err := titleAndBody(c)
	if err != nil {
		return err
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		return createManual(ctx, svc, domain.EntryTypeResearch, title, body, "")
	})
}

func actionDecision(ctx context.Context, c *cli.Command) error {
	title, body, err := titleAndBody(c)
	if err != nil {
		return err
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		return createManual(ctx, svc, domain.EntryTypeDecision, title, body, "")
	})
}

func actionGoal(ctx context.Context, c *cli.Command) error {
	title, body, err := titleAndBody(c)
	if err != nil {
		return err
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		return createManual(ctx, svc, domain.EntryTypeGoal, title, body, domain.StatusOpen)
	})
}

// createManual is the shared body of note / decision / goal.
func createManual(ctx context.Context, svc *repository.Service, typ domain.EntryType, title, body string, status domain.Status) error {
	hash := sha256Hex(typ, title, body)
	e, err := svc.InsertEntry(ctx, domain.Entry{
		Type:      typ,
		Title:     title,
		Body:      body,
		Source:    domain.SourceManual,
		SourceRef: hash,
		Origin:    domain.OriginLocal,
		Status:    status,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ created %s %s\n", typ, e.ID)
	return nil
}

func titleAndBody(c *cli.Command) (string, string, error) {
	args := c.Args().Slice()
	if len(args) == 0 {
		return "", "", errors.New("title required: see --help")
	}
	title := args[0]
	body := ""
	if len(args) > 1 {
		// Join the rest with single spaces. Bodies with embedded newlines
		// can be passed quoted; the CLI does not try to parse markdown.
		for i, a := range args[1:] {
			if i > 0 {
				body += " "
			}
			body += a
		}
	}
	return title, body, nil
}

// --- read commands ---

func actionList(ctx context.Context, c *cli.Command) error {
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		filter := repository.ListFilter{
			Type:   domain.EntryType(c.String("type")),
			RepoID: c.String("repo"),
			Limit:  int(c.Int("limit")),
		}
		// Time-travel branch: --at <RFC3339> dispatches to ListEntriesAt
		// so engineers can ask "what was alive on 6/8 14:00?". Without
		// --at we keep the original "live now" behaviour.
		if atStr := c.String("at"); atStr != "" {
			asOf, err := parseAt(atStr)
			if err != nil {
				return fmt.Errorf("invalid --at: %w", err)
			}
			entries, err := svc.ListEntriesAt(ctx, asOf, filter)
			if err != nil {
				return err
			}
			fmt.Printf("# state at %s\n", asOf.Format(time.RFC3339))
			printEntries(os.Stdout, entries)
			return nil
		}
		entries, err := svc.ListEntries(ctx, filter)
		if err != nil {
			return err
		}
		printEntries(os.Stdout, entries)
		return nil
	})
}

// actionHistory walks the supersede chain for a logical_id and prints each
// version with its occurred_at + ingested_at side by side. The bitemporal
// payoff: engineers can see "decision changed at T1, then again at T2,
// occurring originally on T0".
func actionHistory(ctx context.Context, c *cli.Command) error {
	args := c.Args().Slice()
	if len(args) < 1 {
		return errors.New("usage: workingbad history <logical-id>")
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		history, err := svc.EntryHistory(ctx, args[0])
		if err != nil {
			return err
		}
		if len(history) == 0 {
			fmt.Println("(no history — unknown logical_id)")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "VERSION\tID\tOCCURRED_AT\tINGESTED_AT\tTITLE")
		for _, e := range history {
			marker := ""
			if e.IsCurrent {
				marker = " (current)"
			}
			_, _ = fmt.Fprintf(tw, "v%d\t%s\t%s\t%s\t%s%s\n",
				e.Version, e.ID,
				e.OccurredAt.Format(time.RFC3339),
				e.IngestedAt.Format(time.RFC3339),
				e.Title, marker)
		}
		_ = tw.Flush()
		return nil
	})
}

// parseAt accepts either a full RFC3339 timestamp or a date-only string
// (interpreted as midnight UTC). Relative parsing ("1h ago", "yesterday")
// is intentionally not supported — a tiny scope creep that would only
// confuse the user when timezones get involved. Engineers can compute the
// timestamp themselves.
func parseAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("must be RFC3339 (2026-06-08T14:00:00Z) or date (2026-06-08): %q", s)
}

func actionPending(ctx context.Context, c *cli.Command) error {
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		n, err := svc.CountPendingSegments(ctx, repository.MaterializeScope{})
		if err != nil {
			return err
		}
		fmt.Printf("pending segments: %d\n", n)
		return nil
	})
}

// --- edge ops ---

func actionAttach(ctx context.Context, c *cli.Command) error {
	args := c.Args().Slice()
	if len(args) < 2 {
		return errors.New("usage: workingbad attach <entry-id> <goal-id>")
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		edge, err := svc.AttachToGoal(ctx, args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Printf("✓ attached edge %s\n", edge.ID)
		return nil
	})
}

func actionDetach(ctx context.Context, c *cli.Command) error {
	args := c.Args().Slice()
	if len(args) < 1 {
		return errors.New("usage: workingbad detach <edge-id>")
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		if err := svc.DetachFromGoal(ctx, args[0]); err != nil {
			return err
		}
		fmt.Printf("✓ detached %s\n", args[0])
		return nil
	})
}

func actionStatus(ctx context.Context, c *cli.Command) error {
	args := c.Args().Slice()
	if len(args) < 2 {
		return errors.New("usage: workingbad status <goal-id> <open|in_progress|done|archived>")
	}
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		newGoal, err := svc.SetGoalStatus(ctx, args[0], domain.Status(args[1]))
		if err != nil {
			return err
		}
		fmt.Printf("✓ goal %s status=%s (new id %s)\n", args[0], newGoal.Status, newGoal.ID)
		return nil
	})
}

func actionSummarize(ctx context.Context, c *cli.Command) error {
	return withService(c, func(ctx context.Context, svc *repository.Service) error {
		// Phase 1 ships the mock provider. Real local/api providers arrive
		// in Phase 3 — see ROADMAP.
		provider := mock.New()
		res, err := svc.BatchMaterialize(ctx, repository.MaterializeScope{}, provider)
		if err != nil {
			return err
		}
		fmt.Printf("materialized: %d  failed: %d\n", res.Materialized, res.Failed)
		for _, e := range res.Errors {
			fmt.Printf("  - %v\n", e)
		}
		return nil
	})
}

// --- output helpers ---

func printEntries(w io.Writer, entries []domain.Entry) {
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(w, "(no entries)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tTYPE\tSTATUS\tTITLE")
	for _, e := range entries {
		status := string(e.Status)
		if status == "" {
			status = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.ID, e.Type, status, e.Title)
	}
	_ = tw.Flush()
}

func sha256Hex(typ domain.EntryType, title, body string) string {
	h := sha256.New()
	h.Write([]byte(string(typ)))
	h.Write([]byte{0})
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}
