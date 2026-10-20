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
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/buildinfo"
	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/changes"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/logging"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/reminders"
	"github.com/loafykitten/hekate/service/internal/server"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/tasks"
	"github.com/loafykitten/hekate/service/internal/telegram"
	"github.com/loafykitten/hekate/service/internal/voice"
	"github.com/loafykitten/hekate/service/internal/voice/elevenlabs"
	"github.com/loafykitten/hekate/service/internal/voice/parakeet"
	"github.com/loafykitten/hekate/service/internal/weather"
	"github.com/loafykitten/hekate/service/internal/web"
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

	// Daily-life features. They emit invalidate/notify events through the hub.
	taskSvc, err := tasks.New(tasks.Options{Store: db, Location: cfg.Location, Events: hub, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build tasks: %w", err)
	}
	pushSvc, err := push.New(ctx, push.Options{Store: db, Subject: cfg.PushSubject, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build push: %w", err)
	}
	// Telegram is the backup channel: a notification that web push can't get to
	// any device goes there instead. Its approval buttons are wired up below.
	tg := telegram.New(telegram.Options{Token: cfg.TelegramBotToken.Reveal(), ChatID: cfg.TelegramChatID, Store: db, Logger: logger.Logger})
	notifier := tg.Fallback(pushSvc)
	remSvc, err := reminders.New(reminders.Options{Store: db, Location: cfg.Location, Events: hub, Push: notifier, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build reminders: %w", err)
	}
	memSvc, err := memory.New(memory.Options{Store: db, Location: cfg.Location, Events: hub, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build memory: %w", err)
	}

	// Actions that wait for the owner's say-so. Features register their kinds
	// on approvalSvc before the server starts.
	approvalSvc, err := approvals.New(approvals.Options{Store: db, Location: cfg.Location, Events: hub, Broadcast: hub, Push: notifier, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build approvals: %w", err)
	}
	tg.SetDecider(approvalSvc)

	toolList := slices.Concat(taskSvc.Tools(), remSvc.Tools(), memSvc.Tools())
	sources := []agent.Source{
		{Name: "What you know about {{tamer}}", Source: memSvc},
		{Name: "Waiting on {{tamer}}", Source: approvalSvc},
	}
	api := []func(*http.ServeMux){taskSvc.Routes, remSvc.Routes, pushSvc.Routes, memSvc.Routes, approvalSvc.Routes}

	// World tools: calendar (CalDAV), web search/read, weather.
	invalidate := func(domain string) { hub.Invalidate(domain) }
	calSvc := calendar.New(calendar.Options{
		URL: cfg.CalDAVURL, Username: cfg.CalDAVUsername, Password: cfg.CalDAVPassword,
		CalendarName: cfg.CalDAVCalendar, Location: cfg.Location, Logger: logger.Logger, OnChange: invalidate,
	})
	webSvc := web.New(web.Options{BraveAPIKey: cfg.BraveAPIKey, Logger: logger.Logger})
	weatherSvc := weather.New(weather.Options{
		Settings: db, Units: cfg.Units, Location: cfg.Location, Logger: logger.Logger, OnChange: invalidate,
	})
	toolList = slices.Concat(toolList, calSvc.Tools(), webSvc.Tools(), weatherSvc.Tools())
	sources = append(sources,
		agent.Source{Name: "Calendar", Source: calSvc},
		agent.Source{Name: "Weather at home", Source: weatherSvc},
	)
	api = append(api, calSvc.Register, webSvc.Register, weatherSvc.Register)

	// Mail: IMAP header cache, sweep and classification. Trashing, unsubscribing,
	// new rules and bulk changes go through approvals.
	llmClient := llm.New(llm.Config{
		BaseURL: cfg.NeuralwattBaseURL,
		APIKey:  cfg.NeuralwattAPIKey.Reveal(),
		Logger:  logger.Logger,
	})
	mailSvc, err := mail.New(mail.Options{
		Store: db, Accounts: cfg.MailAccounts, Interval: cfg.MailSweep,
		LLM: llmClient, Model: cfg.ModelBulk, Approvals: approvalSvc,
		OwnerName: cfg.TamerName, Location: cfg.Location, Logger: logger.Logger, OnChange: invalidate,
	})
	if err != nil {
		return fmt.Errorf("build mail: %w", err)
	}
	toolList = slices.Concat(toolList, mailSvc.Tools())
	sources = append(sources, agent.Source{Name: "Mail", Source: mailSvc})
	api = append(api, mailSvc.Routes)

	// Changes she proposes to Hekate itself; the owner approves them and builds
	// them with Claude Code.
	changeSvc, err := changes.New(changes.Options{Store: db, Approvals: approvalSvc, Location: cfg.Location, Events: hub, Push: notifier, Logger: logger.Logger})
	if err != nil {
		return fmt.Errorf("build changes: %w", err)
	}
	toolList = slices.Concat(toolList, changeSvc.Tools())
	sources = append(sources, agent.Source{Name: "Your change requests", Source: changeSvc})
	api = append(api, changeSvc.Routes)

	tools, err := agent.NewRegistry(toolList...)
	if err != nil {
		return fmt.Errorf("register tools: %w", err)
	}
	ag, err := agent.New(agent.Options{
		LLM:       llmClient,
		Store:     db,
		Hub:       hub,
		Synth:     synth,
		Tools:     tools,
		Context:   sources,
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

	// The schedulers run until the process is told to stop.
	var background sync.WaitGroup
	background.Go(func() { remSvc.Run(ctx) })
	background.Go(func() { approvalSvc.Run(ctx) })
	background.Go(func() { mailSvc.Run(ctx) })
	background.Go(func() { tg.Run(ctx) })

	srv := server.New(server.Options{
		Logger:       logger.Logger,
		DB:           db,
		WebDir:       cfg.WebDir,
		AssetsDir:    cfg.AssetsDir,
		Conversation: ag,
		Hub:          hub,
		STT:          stt,
		TTS:          synth != nil,
		API:          api,
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
	background.Wait()
	logger.Info("stopped")
	return nil
}
