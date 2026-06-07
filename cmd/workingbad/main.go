// Command workingbad is the local + LLM semantic truth source for engineers.
// Phase 1 ships the CLI that lets you record notes, decisions, goals; attach
// activities to goals; track goal status; and list the live state. Real
// connectors and the Web UI arrive in later slices.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"
)

const (
	defaultConfigPath = "./config.yaml"
	binaryVersion     = "0.0.0-dev"
)

func main() {
	app := &cli.Command{
		Name:     "workingbad",
		Usage:    "Local + LLM semantic truth source for engineers",
		Version:  binaryVersion,
		Flags:    globalFlags(),
		Commands: allCommands(),
		// Default action with no subcommand: load + migrate + report.
		Action: actionInit,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error("workingbad", "err", err)
		os.Exit(1)
	}
}

func globalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Value:   defaultConfigPath,
			Usage:   "path to config.yaml",
		},
	}
}
