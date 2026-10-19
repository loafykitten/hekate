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

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/buildinfo"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/logging"
	"github.com/loafykitten/hekate/service/internal/server"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/voice"
	"github.com/loafykitten/hekate/service/internal/voice/elevenlabs"
	"github.com/loafykitten/hekate/service/internal/voice/parakeet"
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

	// Voice. Nil means "not configured": replies are text-only and push-to-talk
	// reports stt_unavailable.
	var synth voice.Synthesizer
	if key := cfg.ElevenLabsAPIKey.Reveal(); key != "" {
		synth = elevenlabs.New(key, cfg.ElevenLabsVoiceID, cfg.ElevenLabsModel,
			elevenlabs.WithLogger(logger.Logger))
	}
	var stt voice.Transcriber
	if cfg.STT {
		// Downloads and loads the model in the background; push-to-talk reports
		// stt_unavailable until it's ready.
		t := parakeet.New(ctx, cfg.ModelsDir, logger.Logger)
		defer t.Close()
		stt = t
	}

	hub := server.NewHub(logger.Logger)
	defer hub.Close()

	if cfg.NeuralwattAPIKey == "" {
		logger.Warn("NEURALWATT_API_KEY is not set: she cannot answer until it is")
	}
	ag, err := agent.New(agent.Options{
		LLM: llm.New(llm.Config{
			BaseURL: cfg.NeuralwattBaseURL,
			APIKey:  cfg.NeuralwattAPIKey.Reveal(),
			Logger:  logger.Logger,
		}),
		Store:     db,
		Hub:       hub,
		Synth:     synth,
		Logger:    logger.Logger,
		Tamer:     cfg.TamerName,
		Location:  cfg.Location,
		Chat:      cfg.ModelChat,
		Deep:      cfg.ModelDeep,
		AssetsDir: cfg.AssetsDir,
	})
	if err != nil {
		return fmt.Errorf("build agent: %w", err)
	}
	if err := ag.Start(ctx); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	srv := server.New(server.Options{
		Logger:       logger.Logger,
		DB:           db,
		WebDir:       cfg.WebDir,
		AssetsDir:    cfg.AssetsDir,
		Conversation: ag,
		Hub:          hub,
		STT:          stt,
		TTS:          synth != nil,
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
	hub.Close() // hijacked WebSocket connections are not closed by Shutdown
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := ag.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("agent shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("stopped")
	return nil
}
