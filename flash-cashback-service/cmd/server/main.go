package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embed the timezone database so Asia/Jakarta resolves on a minimal image.
	_ "time/tzdata"

	"flashcashback/config"
	"flashcashback/internal/httpapi"
	"flashcashback/internal/observability"
	"flashcashback/internal/redisx"
	"flashcashback/internal/store"
	"flashcashback/internal/usecase"
)

func main() {
	if err := run(); err != nil {
		// Logger may not exist yet; use stderr directly.
		os.Stderr.WriteString("fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.Logger(cfg.Env)
	metrics := observability.NewMetrics()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Postgres — the source of truth.
	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	logger.Info("migrations applied")

	repo := store.NewRepo(pool)

	// Redis — cache / rate limiter only (non-authoritative).
	rc := redisx.New(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer func() { _ = rc.Close() }()
	if err := rc.Ping(ctx); err != nil {
		logger.Warn("redis unavailable; rate limiting will fail open", "error", err)
	}

	svc := usecase.New(repo, metrics, time.Now)

	router := httpapi.NewRouter(httpapi.Deps{
		Service:            svc,
		Metrics:            metrics,
		Redis:              rc,
		Logger:             logger,
		RateLimitPerMinute: cfg.RateLimitPerMinute,
		AllowedOrigins:     cfg.AllowedOrigins,
		Ready: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			return pool.Ping(pingCtx)
		},
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "port", cfg.Port, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Graceful shutdown: stop accepting, let in-flight requests finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return err
	}
	logger.Info("server stopped cleanly")
	return nil
}
