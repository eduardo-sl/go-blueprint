package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
)

const (
	_maxConns     = 25
	_minConns     = 5
	_maxConnLife  = 5 * time.Minute
	_maxConnIdle  = 1 * time.Minute
	_healthPeriod = 1 * time.Minute
)

func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("database: parse config: %w", err)
	}

	cfg.MaxConns = _maxConns
	cfg.MinConns = _minConns
	cfg.MaxConnLifetime = _maxConnLife
	cfg.MaxConnIdleTime = _maxConnIdle
	cfg.HealthCheckPeriod = _healthPeriod
	cfg.ConnConfig.Tracer = telemetry.NewPgxTracer()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}

	return pool, nil
}
