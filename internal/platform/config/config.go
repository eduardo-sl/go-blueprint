package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	Env                  string        `mapstructure:"env"`
	Addr                 string        `mapstructure:"addr"`
	DatabaseURL          string        `mapstructure:"database_url"`
	EventLogPath         string        `mapstructure:"event_log_path"`
	JWTSecret            string        `mapstructure:"jwt_secret"`
	JWTExpiry            time.Duration `mapstructure:"jwt_expiry"`
	LogLevel             string        `mapstructure:"log_level"`
	RedisAddr            string        `mapstructure:"redis_addr"`
	RedisPassword        string        `mapstructure:"redis_password"`
	RedisDB              int           `mapstructure:"redis_db"`
	CacheTTL             time.Duration `mapstructure:"cache_ttl"`
	WorkerCount          int           `mapstructure:"worker_count"`
	WorkerQueue          int           `mapstructure:"worker_queue"`
	WorkerDrainTimeout   time.Duration `mapstructure:"worker_drain_timeout"`
	OutboxInterval       int           `mapstructure:"outbox_interval"` // seconds
	OutboxBatch          int           `mapstructure:"outbox_batch"`
	OutboxMaxAttempts    int           `mapstructure:"outbox_max_attempts"`
	OTelEnabled          bool          `mapstructure:"otel_enabled"`
	OTelServiceName      string        `mapstructure:"otel_service_name"`
	OTelEndpoint         string        `mapstructure:"otel_endpoint"`
	MetricsAddr          string        `mapstructure:"metrics_addr"`
	GRPCEnabled          bool          `mapstructure:"grpc_enabled"`
	GRPCAddr             string        `mapstructure:"grpc_addr"`
	MongoURI             string        `mapstructure:"mongo_uri"`
	MongoDatabase        string        `mapstructure:"mongo_database"`
	KafkaEnabled         bool          `mapstructure:"kafka_enabled"`
	KafkaBrokers         string        `mapstructure:"kafka_brokers"`
	KafkaTopicCustomers  string        `mapstructure:"kafka_topic_customers"`
	KafkaDLQTopic        string        `mapstructure:"kafka_dlq_topic"`
	KafkaConsumerGroup   string        `mapstructure:"kafka_consumer_group"`
	KafkaProducerRetries int           `mapstructure:"kafka_producer_retries"`
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
	v.SetDefault("worker_drain_timeout", "15s")
	v.SetDefault("outbox_interval", 5)
	v.SetDefault("outbox_batch", 50)
	v.SetDefault("outbox_max_attempts", 5)
	v.SetDefault("otel_enabled", false)
	v.SetDefault("otel_service_name", "go-blueprint")
	v.SetDefault("otel_endpoint", "localhost:4318")
	v.SetDefault("metrics_addr", ":9091")
	v.SetDefault("grpc_enabled", false)
	v.SetDefault("grpc_addr", ":9090")
	v.SetDefault("mongo_uri", "mongodb://localhost:27017")
	v.SetDefault("mongo_database", "go_blueprint")
	v.SetDefault("kafka_enabled", false)
	v.SetDefault("kafka_brokers", "localhost:9092")
	v.SetDefault("kafka_topic_customers", "customers.events")
	v.SetDefault("kafka_dlq_topic", "customers.events.dlq")
	v.SetDefault("kafka_consumer_group", "go-blueprint")
	v.SetDefault("kafka_producer_retries", 3)

	_ = v.ReadInConfig()

	// AutomaticEnv resolves any key viper knows about, and SetDefault is what
	// makes a key known — so the defaulted keys above need no explicit binding.
	// Only three do: two have no default and would be invisible to Unmarshal,
	// and one answers to an environment name the key replacer cannot derive.
	if err := v.BindEnv("database_url", "DATABASE_URL"); err != nil {
		return nil, fmt.Errorf("config: bind DATABASE_URL: %w", err)
	}
	if err := v.BindEnv("jwt_secret", "JWT_SECRET"); err != nil {
		return nil, fmt.Errorf("config: bind JWT_SECRET: %w", err)
	}
	// OTEL_EXPORTER_OTLP_ENDPOINT is the name the OTel spec gives this variable;
	// the replacer would look for OTEL_ENDPOINT, so the binding stays.
	if err := v.BindEnv("otel_endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT"); err != nil {
		return nil, fmt.Errorf("config: bind OTEL_EXPORTER_OTLP_ENDPOINT: %w", err)
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
