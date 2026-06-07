// Command workingbad is the local + LLM semantic truth source for engineers.
// Phase 0 only loads config + opens the SQLite store + applies migrations,
// proving the geological assumptions. Pipeline behaviour arrives in Phase 1.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/migrations"
	"github.com/Leon180/workingbad/internal/repository"
)

const (
	defaultConfigPath = "./config.yaml"
	binaryVersion     = "0.0.0-dev"
)

func main() {
	app := &cli.Command{
		Name:    "workingbad",
		Usage:   "Local + LLM semantic truth source for engineers",
		Version: binaryVersion,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Value:   defaultConfigPath,
				Usage:   "path to config.yaml",
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "migrate",
				Usage:  "Apply forward-only migrations and exit; reports applied version",
				Action: actionMigrate,
			},
			{
				Name:   "version",
				Usage:  "Print binary and (if available) migration version",
				Action: actionVersion,
			},
		},
		// Default action with no subcommand: load + migrate + exit 0.
		// Doubles as the Phase 0 exit check: "binary starts, config validates,
		// DB schema is built, clean exit".
		Action: actionInit,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error("workingbad", "err", err)
		os.Exit(1)
	}
}

// actionInit is the Phase 0 acceptance path: load config, open DB (which
// auto-migrates), report state, exit clean.
func actionInit(_ context.Context, c *cli.Command) error {
	cfg, db, version, err := loadAndOpen(c.String("config"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	fmt.Printf(
		"workingbad %s\n  config = %s\n  db     = %s\n  migration_version = %d\n  ai_kind = %s\n",
		binaryVersion, c.String("config"), cfg.DB.Path, version, cfg.AI.Kind,
	)
	return nil
}

func actionMigrate(_ context.Context, c *cli.Command) error {
	_, db, version, err := loadAndOpen(c.String("config"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	fmt.Printf("migrations: version=%d\n", version)
	return nil
}

// actionVersion intentionally tolerates a missing config — the binary should
// still print its own version when run on a host that has not been configured.
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

	version, err := migrations.Version(db)
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}
	fmt.Printf("workingbad %s (migration version %d)\n", binaryVersion, version)
	return nil
}

// loadAndOpen centralises the config → repository.Open → migrations.Version
// flow used by every action. Returns the populated trio plus any error.
func loadAndOpen(path string) (*config.Config, *sql.DB, int64, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("config: %w", err)
	}
	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("repository: %w", err)
	}
	version, err := migrations.Version(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, 0, fmt.Errorf("migration version: %w", err)
	}
	return cfg, db, version, nil
}
