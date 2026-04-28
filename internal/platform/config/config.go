package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	Env          string        `mapstructure:"env"`
	Addr         string        `mapstructure:"addr"`
	DatabaseURL  string        `mapstructure:"database_url"`
	EventLogPath string        `mapstructure:"event_log_path"`
	JWTSecret    string        `mapstructure:"jwt_secret"`
	JWTExpiry    time.Duration `mapstructure:"jwt_expiry"`
	LogLevel     string        `mapstructure:"log_level"`
	RedisAddr     string        `mapstructure:"redis_addr"`
	RedisPassword string        `mapstructure:"redis_password"`
	RedisDB       int           `mapstructure:"redis_db"`
	CacheTTL      time.Duration `mapstructure:"cache_ttl"`
	WorkerCount    int `mapstructure:"worker_count"`
	WorkerQueue    int `mapstructure:"worker_queue"`
	OutboxInterval int `mapstructure:"outbox_interval"` // seconds
	OutboxBatch    int `mapstructure:"outbox_batch"`
	OTelEnabled     bool   `mapstructure:"otel_enabled"`
	OTelServiceName string `mapstructure:"otel_service_name"`
	OTelEndpoint    string `mapstructure:"otel_endpoint"`
	MetricsAddr     string `mapstructure:"metrics_addr"`
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("env", "development")
	v.SetDefault("addr", ":8080")
	v.SetDefault("event_log_path", "./data/events.db")
	v.SetDefault("jwt_expiry", "24h")
	v.SetDefault("log_level", "info")
	v.SetDefault("redis_addr", "")
	v.SetDefault("redis_password", "")
	v.SetDefault("redis_db", 0)
	v.SetDefault("cache_ttl", "5m")
	v.SetDefault("worker_count", 4)
	v.SetDefault("worker_queue", 100)
	v.SetDefault("outbox_interval", 5)
	v.SetDefault("outbox_batch", 50)
	v.SetDefault("otel_enabled", false)
	v.SetDefault("otel_service_name", "go-blueprint")
	v.SetDefault("otel_endpoint", "localhost:4318")
	v.SetDefault("metrics_addr", ":9091")

	_ = v.ReadInConfig()

	if err := v.BindEnv("database_url", "DATABASE_URL"); err != nil {
		return nil, fmt.Errorf("config: bind DATABASE_URL: %w", err)
	}
	if err := v.BindEnv("jwt_secret", "JWT_SECRET"); err != nil {
		return nil, fmt.Errorf("config: bind JWT_SECRET: %w", err)
	}
	if err := v.BindEnv("redis_addr", "REDIS_ADDR"); err != nil {
		return nil, fmt.Errorf("config: bind REDIS_ADDR: %w", err)
	}
	if err := v.BindEnv("redis_password", "REDIS_PASSWORD"); err != nil {
		return nil, fmt.Errorf("config: bind REDIS_PASSWORD: %w", err)
	}
	if err := v.BindEnv("redis_db", "REDIS_DB"); err != nil {
		return nil, fmt.Errorf("config: bind REDIS_DB: %w", err)
	}
	if err := v.BindEnv("cache_ttl", "CACHE_TTL"); err != nil {
		return nil, fmt.Errorf("config: bind CACHE_TTL: %w", err)
	}
	if err := v.BindEnv("otel_enabled", "OTEL_ENABLED"); err != nil {
		return nil, fmt.Errorf("config: bind OTEL_ENABLED: %w", err)
	}
	if err := v.BindEnv("otel_service_name", "OTEL_SERVICE_NAME"); err != nil {
		return nil, fmt.Errorf("config: bind OTEL_SERVICE_NAME: %w", err)
	}
	if err := v.BindEnv("otel_endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT"); err != nil {
		return nil, fmt.Errorf("config: bind OTEL_EXPORTER_OTLP_ENDPOINT: %w", err)
	}
	if err := v.BindEnv("metrics_addr", "METRICS_ADDR"); err != nil {
		return nil, fmt.Errorf("config: bind METRICS_ADDR: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return fmt.Errorf("config: JWT_SECRET is required")
	}
	return nil
}
