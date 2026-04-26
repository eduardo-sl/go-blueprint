package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	echoswagger "github.com/swaggo/echo-swagger"

	_ "github.com/eduardo-sl/go-blueprint/docs"
	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	appmiddleware "github.com/eduardo-sl/go-blueprint/internal/platform/middleware"
)

const _shutdownTimeout = 10 * time.Second

// @title           Go Blueprint API
// @version         1.0
// @description     Idiomatic Go REST API demonstrating DDD, CQRS, and Clean Architecture
// @host            localhost:8080
// @BasePath        /api/v1
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func Start(
	cfg *config.Config,
	customerHandler *customer.Handler,
	authHandler *auth.Handler,
	logger *slog.Logger,
) error {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	appmiddleware.Register(e, logger)

	api := e.Group("/api/v1")

	authHandler.RegisterRoutes(api)

	protected := api.Group("", auth.JWTMiddleware(cfg.JWTSecret))
	customerHandler.RegisterRoutes(protected)

	e.GET("/health", healthCheck(cfg))
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		logger.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), _shutdownTimeout)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}

	logger.Info("server stopped gracefully")
	return nil
}

var _startTime = time.Now()

func healthCheck(cfg *config.Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"version": "1.0.0",
			"uptime":  time.Since(_startTime).String(),
			"env":     cfg.Env,
		})
	}
}
