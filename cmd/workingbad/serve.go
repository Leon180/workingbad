package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/repository"
	"github.com/Leon180/workingbad/internal/web"
)

// actionServe wires the loopback Web UI: load config, open DB (auto-
// migrate), build repository service, start net/http server. Blocks until
// the user sends SIGINT/SIGTERM, then drains in flight requests.
//
// With --debug: also starts a second listener on 127.0.0.1:<debug-port>
// (default 6060) exposing /debug/pprof endpoints for one-shot profiling.
// pprof is NEVER registered on the main mux — that would leak handlers
// even when --debug is off; it gets its own mux+listener so the cost
// in the default path is zero.
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if c.Bool("debug") {
		stop, err := startPprofServer(int(c.Int("debug-port")))
		if err != nil {
			return fmt.Errorf("debug: %w", err)
		}
		defer stop()
	}

	fmt.Println("press ctrl-c to stop")
	return srv.Serve(ctx)
}

// startPprofServer binds a second http.Server to 127.0.0.1:port and
// registers ONLY the pprof handlers. Loopback-only is non-negotiable:
// pprof exposes goroutine stacks, allocations, and live profiling
// data — none of which should leave the machine. Listener is opened
// up-front so we can fail fast if the port is busy.
//
// The returned stop func triggers a graceful shutdown with a small
// drain window.
func startPprofServer(port int) (func(), error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("debug-port out of range: %d", port)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("debug listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Read timeouts — pprof requests like /profile?seconds=30 deliberately
	// hold the connection open for the profile window. ReadHeaderTimeout
	// caps the header phase; no ReadTimeout/WriteTimeout so long-poll
	// profile endpoints work.
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		fmt.Printf("pprof debug surface at http://%s/debug/pprof/\n", addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("pprof debug surface: %v\n", err)
		}
	}()

	stop := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	return stop, nil
}
