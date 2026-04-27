// Package main is the entry point for the go-blueprint API server.
//
//	@title			Go Blueprint API
//	@version		1.0
//	@description	Idiomatic Go REST API demonstrating DDD, CQRS, and Clean Architecture
//	@host			localhost:8080
//	@BasePath		/api/v1
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database/postgres"
	"github.com/eduardo-sl/go-blueprint/internal/platform/server"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	logger := newLogger(cfg.Env, cfg.LogLevel)
	slog.SetDefault(logger)

	// Root context — cancelled on OS signal to trigger graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	if err := runMigrations(cfg.DatabaseURL); err != nil {
		logger.Error("migrations failed", slog.Any("error", err))
		os.Exit(1)
	}

	if err := os.MkdirAll("data", 0750); err != nil {
		logger.Error("failed to create data directory", slog.Any("error", err))
		os.Exit(1)
	}

	eventLog, err := eventlog.NewSQLiteStore(cfg.EventLogPath)
	if err != nil {
		logger.Error("failed to init event log", slog.Any("error", err))
		os.Exit(1)
	}

	var customerCache cache.Cache = cache.NoopCache{}
	if cfg.RedisAddr != "" {
		rc, err := cache.NewRedisCache(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, logger)
		if err != nil {
			logger.Warn("redis unavailable, cache disabled", slog.Any("error", err))
		} else {
			customerCache = rc
		}
	}

	// Step 1 of graceful shutdown: worker pool — started first, stopped last.
	workerPool := worker.New(ctx, cfg.WorkerCount, cfg.WorkerQueue, logger)

	// Outbox store and poller
	outboxStore := outbox.NewPostgresStore(pool)
	publisher := outbox.NewLogPublisher(logger) // swap for KafkaPublisher in production
	poller := outbox.NewPoller(
		outboxStore,
		publisher,
		workerPool,
		time.Duration(cfg.OutboxInterval)*time.Second,
		cfg.OutboxBatch,
		logger,
	)
	// Poller exits when ctx is cancelled (step 3 of graceful shutdown).
	go poller.Run(ctx)

	customerRepo := postgres.NewCustomerRepository(pool)
	customerSvc := customer.NewService(customerRepo, pool, outboxStore, eventLog, customerCache, logger)
	customerQuery := customer.NewCachedQueryService(
		customer.NewQueryService(customerRepo),
		customerCache,
		cfg.CacheTTL,
		logger,
	)
	customerHandler := customer.NewHandler(customerSvc, customerQuery)

	userRepo := postgres.NewUserRepository(pool)
	authSvc := auth.NewService(userRepo, cfg.JWTSecret, cfg.JWTExpiry, logger)
	authHandler := auth.NewHandler(authSvc)

	// Step 2: HTTP server drains active requests with a 10-second timeout.
	// server.Start blocks until ctx is cancelled, then shuts down Echo.
	if err := server.Start(ctx, cfg, customerHandler, authHandler, customerCache, workerPool, logger); err != nil {
		logger.Error("server error", slog.Any("error", err))
		os.Exit(1)
	}

	// Step 3: worker pool drains queued jobs and waits for in-flight deliveries.
	// The outbox poller already exited because ctx was cancelled before this point.
	workerPool.Stop()
	logger.Info("worker pool drained")
	// Step 4: Postgres pool closed via defer pool.Close() above.
	// Step 5: process exits 0.
}

func newLogger(env, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if env == "production" || env == "prod" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func runMigrations(dsn string) error {
	db, err := goose.OpenDBWithDriver("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	return goose.Up(db, "migrations")
}
