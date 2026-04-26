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

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database/postgres"
	"github.com/eduardo-sl/go-blueprint/internal/platform/server"
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

	ctx := context.Background()

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

	customerRepo := postgres.NewCustomerRepository(pool)
	customerSvc := customer.NewService(customerRepo, eventLog, logger)
	customerQuery := customer.NewQueryService(customerRepo)
	customerHandler := customer.NewHandler(customerSvc, customerQuery)

	userRepo := postgres.NewUserRepository(pool)
	authSvc := auth.NewService(userRepo, cfg.JWTSecret, cfg.JWTExpiry, logger)
	authHandler := auth.NewHandler(authSvc)

	if err := server.Start(cfg, customerHandler, authHandler, logger); err != nil {
		logger.Error("server error", slog.Any("error", err))
		os.Exit(1)
	}
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
