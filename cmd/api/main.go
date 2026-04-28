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
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database/mongodb"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database/postgres"
	grpcserver "github.com/eduardo-sl/go-blueprint/internal/platform/grpc"
	kafkaplatform "github.com/eduardo-sl/go-blueprint/internal/platform/kafka"
	"github.com/eduardo-sl/go-blueprint/internal/platform/server"
	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
	"github.com/eduardo-sl/go-blueprint/internal/product"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	// Root context — cancelled on OS signal to trigger graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Init OTel SDK when enabled. Provider.Shutdown() is called explicitly below,
	// before the Postgres pool closes, to flush in-flight spans that include DB child spans.
	var telProv *telemetry.Provider
	if cfg.OTelEnabled {
		telProv, err = telemetry.Setup(ctx, cfg)
		if err != nil {
			logger.Error("failed to init telemetry", slog.Any("error", err))
			os.Exit(1)
		}
		// Re-initialize metric instruments bound to the real SDK meter provider.
		if err := telemetry.InitMetrics(); err != nil {
			logger.Error("failed to init metrics", slog.Any("error", err))
			os.Exit(1)
		}
	}

	// Metrics server — separate port, not publicly exposed with the API.
	var metricsSrv *http.Server
	if cfg.OTelEnabled {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux}
		go func() {
			logger.Info("metrics server starting", slog.String("addr", cfg.MetricsAddr))
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics server error", slog.Any("error", err))
			}
		}()
	}

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

	// Outbox store and publisher.
	// Default publisher is LogPublisher (dev/test). Replaced by KafkaProducer when enabled.
	outboxStore := outbox.NewPostgresStore(pool)
	var publisher outbox.Publisher = outbox.NewLogPublisher(logger)

	if cfg.KafkaEnabled {
		brokers := strings.Split(cfg.KafkaBrokers, ",")

		dlqWriter, err := kafkaplatform.NewDLQWriter(brokers, cfg.KafkaDLQTopic, logger)
		if err != nil {
			logger.Error("failed to init kafka DLQ writer", slog.Any("error", err))
			os.Exit(1)
		}
		defer dlqWriter.Close()

		kafkaProducer, err := kafkaplatform.NewProducer(
			brokers, cfg.KafkaTopicCustomers, dlqWriter, cfg.KafkaProducerRetries, logger,
		)
		if err != nil {
			logger.Error("failed to init kafka producer", slog.Any("error", err))
			os.Exit(1)
		}
		defer kafkaProducer.Close()
		publisher = kafkaProducer

		eventHandler := customer.NewEventHandler(logger)
		kafkaConsumer, err := kafkaplatform.NewConsumer(
			brokers, cfg.KafkaConsumerGroup, cfg.KafkaTopicCustomers, eventHandler, logger,
		)
		if err != nil {
			logger.Error("failed to init kafka consumer", slog.Any("error", err))
			os.Exit(1)
		}
		go kafkaConsumer.Run(ctx)
		defer kafkaConsumer.Close()

		logger.Info("kafka enabled",
			slog.String("brokers", cfg.KafkaBrokers),
			slog.String("topic", cfg.KafkaTopicCustomers),
			slog.String("group", cfg.KafkaConsumerGroup),
		)
	}

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

	// MongoDB — Product Catalog and Customer Preferences.
	mongoClient, err := mongodb.NewClient(ctx, cfg.MongoURI)
	if err != nil {
		logger.Error("failed to connect to mongodb", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = mongoClient.Disconnect(context.Background()) }()

	mongoDB := mongoClient.Database(cfg.MongoDatabase)
	if err := mongodb.EnsureIndexes(ctx, mongoDB); err != nil {
		logger.Error("mongodb index creation failed", slog.Any("error", err))
		os.Exit(1)
	}

	productRepo := mongodb.NewProductRepository(mongoDB)
	productSvc := product.NewService(productRepo, logger)
	productQuery := product.NewQueryService(productRepo)
	productHandler := product.NewHandler(productSvc, productQuery)

	preferencesRepo := mongodb.NewPreferencesRepository(mongoDB)
	preferencesHandler := customer.NewPreferencesHandler(
		customer.NewPreferencesService(preferencesRepo),
	)

	// gRPC server — started only when GRPC_ENABLED=true (default false).
	var grpcSrv *grpc.Server
	if cfg.GRPCEnabled {
		grpcHandler := customer.NewGRPCHandler(customerSvc, customerQuery, eventLog)
		grpcSrv = grpcserver.NewServer(grpcHandler, authSvc, logger)

		lis, err := net.Listen("tcp", cfg.GRPCAddr)
		if err != nil {
			logger.Error("grpc: listener failed", slog.String("addr", cfg.GRPCAddr), slog.Any("error", err))
			os.Exit(1)
		}
		go func() {
			logger.Info("grpc server starting", slog.String("addr", cfg.GRPCAddr))
			if err := grpcSrv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				logger.Error("grpc server error", slog.Any("error", err))
			}
		}()
	}

	// Step 2: HTTP server drains active requests with a 10-second timeout.
	// server.Start blocks until ctx is cancelled, then shuts down Echo.
	if err := server.Start(ctx, cfg, customerHandler, preferencesHandler, authHandler, productHandler, customerCache, workerPool, logger); err != nil {
		logger.Error("server error", slog.Any("error", err))
		os.Exit(1)
	}

	// Graceful shutdown for gRPC — called after HTTP drains so in-flight unary
	// calls complete before the server stops accepting new ones.
	if grpcSrv != nil {
		grpcSrv.GracefulStop()
		logger.Info("grpc server stopped")
	}

	// Step 3: worker pool drains queued jobs and waits for in-flight deliveries.
	// The outbox poller already exited because ctx was cancelled before this point.
	workerPool.Stop()
	logger.Info("worker pool drained")

	// Step 4: flush OTel spans and metrics before closing the database pool.
	// Pending spans include DB child spans — the tracer provider must still be
	// running when the pgx pool closes so those spans are exported correctly.
	if telProv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telProv.Shutdown(shutCtx); err != nil {
			logger.Error("telemetry shutdown error", slog.Any("error", err))
		}
		logger.Info("telemetry flushed")
	}
	if metricsSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(shutCtx)
	}

	// Step 5: Postgres pool closed via defer pool.Close() above.
	// Step 6: process exits 0.
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}
	var base slog.Handler
	if cfg.Env == "production" || cfg.Env == "prod" {
		base = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		base = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(telemetry.NewOTelHandler(base))
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
