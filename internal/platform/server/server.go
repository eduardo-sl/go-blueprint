package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echoswagger "github.com/swaggo/echo-swagger"

	_ "github.com/eduardo-sl/go-blueprint/docs"
	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	appmiddleware "github.com/eduardo-sl/go-blueprint/internal/platform/middleware"
	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
	"github.com/eduardo-sl/go-blueprint/internal/product"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
)

const _shutdownTimeout = 10 * time.Second

// CachePinger is satisfied by cache.RedisCache and cache.NoopCache.
// Defined here so server does not import the cache package directly.
type CachePinger interface {
	Ping(ctx context.Context) error
}

// Start configures Echo, registers routes, starts the HTTP server, and blocks
// until ctx is cancelled. It then drains in-flight requests with a 10-second
// timeout before returning.
//
// Graceful shutdown sequence (orchestrated by the caller, not by this function):
//  1. OS signal received → caller cancels root ctx
//  2. Start returns after Echo drains active requests (10s timeout)
//  3. Caller calls worker.Pool.Stop() — drains queued jobs, waits for in-flight
//  4. Caller defers pool.Close() — Postgres connections released
//  5. Process exits 0
func Start(
	ctx context.Context,
	cfg *config.Config,
	customerHandler *customer.Handler,
	preferencesHandler *customer.PreferencesHandler,
	authHandler *auth.Handler,
	productHandler *product.Handler,
	appCache CachePinger,
	workerPool *worker.Pool,
	logger *slog.Logger,
) error {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	appmiddleware.Register(e, logger)
	e.Use(telemetry.EchoMiddleware(cfg.OTelServiceName))

	api := e.Group("/api/v1")

	authHandler.RegisterRoutes(api)

	protected := api.Group("", auth.JWTMiddleware(cfg.JWTSecret))
	customerHandler.RegisterRoutes(protected)
	preferencesHandler.RegisterPreferencesRoutes(protected)
	productHandler.RegisterRoutes(protected)

	e.GET("/health", healthCheck(cfg, appCache))
	e.GET("/swagger/*", echoswagger.WrapHandler)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      e,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", slog.String("addr", cfg.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server: listen: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining requests")
	}

	// Step 2: stop accepting new connections and drain active requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), _shutdownTimeout)
	defer cancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}

	logger.Info("server stopped gracefully")
	return nil
}

var _startTime = time.Now()

func healthCheck(cfg *config.Config, appCache CachePinger) echo.HandlerFunc {
	return func(c echo.Context) error {
		cacheStatus := "ok"
		if err := appCache.Ping(c.Request().Context()); err != nil {
			cacheStatus = "degraded"
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"version": "1.0.0",
			"uptime":  time.Since(_startTime).String(),
			"env":     cfg.Env,
			"cache":   cacheStatus,
		})
	}
}
