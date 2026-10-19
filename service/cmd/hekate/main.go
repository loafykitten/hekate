// Command hekate runs the Hekate service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/loafykitten/hekate/service/internal/buildinfo"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/logging"
	"github.com/loafykitten/hekate/service/internal/server"
	"github.com/loafykitten/hekate/service/internal/store"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		// Logging may not be up yet, so fail on stderr as well.
		fmt.Fprintln(os.Stderr, "hekate: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger, err := logging.New(cfg.DataDir, cfg.LogLevel, os.Stdout)
	if err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func() { _ = logger.Close() }()
	slog.SetDefault(logger.Logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting",
		"version", buildinfo.Version, "commit", buildinfo.Commit, "built_at", buildinfo.BuiltAt,
		"config", cfg)

	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	srv := server.New(server.Options{
		Logger:    logger.Logger,
		DB:        db,
		WebDir:    cfg.WebDir,
		AssetsDir: cfg.AssetsDir,
	})
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No write timeout: WebSocket and streaming responses are long-lived.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	logger.Info("listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	stop() // a second signal now kills the process the default way

	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("stopped")
	return nil
}
