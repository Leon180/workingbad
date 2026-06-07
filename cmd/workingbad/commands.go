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
		{Name: "migrate", Usage: "apply forward-only migrations and exit; reports applied version", Action: actionMigrate},
		{Name: "version", Usage: "print binary and (if available) migration version", Action: actionVersion},

		// Phase 1 dogfooding surface.
		{
			Name:      "note",
			Usage:     "create a research entry (manual)",
			ArgsUsage: "<title> [body ...]",
			Action:    actionNote,
		},
		{
			Name:      "decision",
			Usage:     "create a decision entry (manual)",
			ArgsUsage: "<title> [body ...]",
			Action:    actionDecision,
		},
		{
			Name:      "goal",
			Usage:     "create a goal entry (status defaults to open)",
			ArgsUsage: "<title> [body ...]",
			Action:    actionGoal,
		},
		{
			Name:  "list",
			Usage: "list live entries newest first",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "type", Usage: "filter by entry type (activity|research|discuss|decision|goal)"},
				&cli.StringFlag{Name: "repo", Usage: "filter by repo_id"},
				&cli.IntFlag{Name: "limit", Value: 20, Usage: "max rows to print"},
			},
			Action: actionList,
		},
		{
			Name:      "attach",
			Usage:     "attach an entry to a goal via part_of edge",
			ArgsUsage: "<entry-id> <goal-id>",
			Action:    actionAttach,
		},
		{
			Name:      "detach",
			Usage:     "mark an attach edge as detached",
			ArgsUsage: "<edge-id>",
			Action:    actionDetach,
		},
		{
			Name:      "status",
			Usage:     "set a goal's status",
			ArgsUsage: "<goal-id> <open|in_progress|done|archived>",
			Action:    actionStatus,
		},
		{Name: "pending", Usage: "count segments needing materialize", Action: actionPending},
		{
			Name:   "summarize",
			Usage:  "materialise all pending segments (Phase 1: uses mock AIProvider)",
			Action: actionSummarize,
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
		entries, err := svc.ListEntries(ctx, filter)
		if err != nil {
			return err
		}
		printEntries(os.Stdout, entries)
		return nil
	})
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
