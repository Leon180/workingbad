package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/repository"
	"github.com/Leon180/workingbad/internal/web"
)

// actionServe wires the loopback Web UI: load config, open DB (auto-
// migrate), build repository service, start net/http server. Blocks until
// the user sends SIGINT/SIGTERM, then drains in flight requests.
func actionServe(_ context.Context, c *cli.Command) error {
	cfg, err := config.LoadFile(c.String("config"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if override := int(c.Int("port")); override > 0 {
		cfg.Web.Port = override
	}

	db, err := repository.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	defer func() { _ = db.Close() }()
	svc := repository.NewService(db)

	srv, err := web.NewServer(svc, cfg.Web)
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}
	fmt.Printf("workingbad serving at http://%s\n", srv.Addr())
	fmt.Println("press ctrl-c to stop")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return srv.Serve(ctx)
}
