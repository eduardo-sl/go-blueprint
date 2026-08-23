package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
)

// _requiredEnv is the minimum needed to get past validate(). Every case sets it
// so a test can assert on the field it actually cares about.
var _requiredEnv = map[string]string{
	"DATABASE_URL": "postgres://user:pass@localhost:5432/db?sslmode=disable",
	"JWT_SECRET":   "test-secret-at-least-32-characters-long",
}

func setEnv(t *testing.T, extra map[string]string) {
	t.Helper()
	for k, v := range _requiredEnv {
		t.Setenv(k, v)
	}
	for k, v := range extra {
		t.Setenv(k, v)
	}
}

// TestLoad_ResolvesEveryEnvVar is the test that earns the removal of twenty
// BindEnv calls. Each case sets one documented variable and asserts the field it
// lands in: if AutomaticEnv does not in fact cover a defaulted key, the case for
// that key fails rather than the variable silently going unread in production.
//
// The table mirrors .env.example. A variable added there without a case here is
// a variable nothing proves is wired.
func TestLoad_ResolvesEveryEnvVar(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(*testing.T, *config.Config)
	}{
		{
			name:  "ENV",
			env:   map[string]string{"ENV": "production"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "production", c.Env) },
		},
		{
			name:  "ADDR",
			env:   map[string]string{"ADDR": ":9999"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, ":9999", c.Addr) },
		},
		{
			name:  "LOG_LEVEL",
			env:   map[string]string{"LOG_LEVEL": "debug"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "debug", c.LogLevel) },
		},
		{
			name: "DATABASE_URL",
			env:  map[string]string{"DATABASE_URL": "postgres://custom@host:5432/other"},
			check: func(t *testing.T, c *config.Config) {
				assert.Equal(t, "postgres://custom@host:5432/other", c.DatabaseURL)
			},
		},
		{
			name:  "EVENT_LOG_PATH",
			env:   map[string]string{"EVENT_LOG_PATH": "/var/lib/events.db"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "/var/lib/events.db", c.EventLogPath) },
		},
		{
			name: "JWT_SECRET",
			env:  map[string]string{"JWT_SECRET": "another-secret-that-is-long-enough"},
			check: func(t *testing.T, c *config.Config) {
				assert.Equal(t, "another-secret-that-is-long-enough", c.JWTSecret)
			},
		},
		{
			name:  "JWT_EXPIRY",
			env:   map[string]string{"JWT_EXPIRY": "1h30m"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 90*time.Minute, c.JWTExpiry) },
		},
		{
			name:  "REDIS_ADDR",
			env:   map[string]string{"REDIS_ADDR": "redis:6379"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "redis:6379", c.RedisAddr) },
		},
		{
			name:  "REDIS_PASSWORD",
			env:   map[string]string{"REDIS_PASSWORD": "hunter2"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "hunter2", c.RedisPassword) },
		},
		{
			name:  "REDIS_DB",
			env:   map[string]string{"REDIS_DB": "3"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 3, c.RedisDB) },
		},
		{
			name:  "CACHE_TTL",
			env:   map[string]string{"CACHE_TTL": "90s"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 90*time.Second, c.CacheTTL) },
		},
		{
			name:  "WORKER_COUNT",
			env:   map[string]string{"WORKER_COUNT": "16"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 16, c.WorkerCount) },
		},
		{
			name:  "WORKER_QUEUE",
			env:   map[string]string{"WORKER_QUEUE": "512"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 512, c.WorkerQueue) },
		},
		{
			name:  "WORKER_DRAIN_TIMEOUT",
			env:   map[string]string{"WORKER_DRAIN_TIMEOUT": "45s"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 45*time.Second, c.WorkerDrainTimeout) },
		},
		{
			name:  "OUTBOX_INTERVAL",
			env:   map[string]string{"OUTBOX_INTERVAL": "11"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 11, c.OutboxInterval) },
		},
		{
			name:  "OUTBOX_BATCH",
			env:   map[string]string{"OUTBOX_BATCH": "200"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 200, c.OutboxBatch) },
		},
		{
			name:  "OUTBOX_MAX_ATTEMPTS",
			env:   map[string]string{"OUTBOX_MAX_ATTEMPTS": "9"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 9, c.OutboxMaxAttempts) },
		},
		{
			name:  "OTEL_ENABLED",
			env:   map[string]string{"OTEL_ENABLED": "true"},
			check: func(t *testing.T, c *config.Config) { assert.True(t, c.OTelEnabled) },
		},
		{
			name:  "OTEL_SERVICE_NAME",
			env:   map[string]string{"OTEL_SERVICE_NAME": "other-service"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "other-service", c.OTelServiceName) },
		},
		{
			// The one key whose env name the replacer cannot derive, which is
			// why its BindEnv survived the cull.
			name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
			env:   map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4318"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "collector:4318", c.OTelEndpoint) },
		},
		{
			name:  "METRICS_ADDR",
			env:   map[string]string{"METRICS_ADDR": ":9999"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, ":9999", c.MetricsAddr) },
		},
		{
			name:  "GRPC_ENABLED",
			env:   map[string]string{"GRPC_ENABLED": "true"},
			check: func(t *testing.T, c *config.Config) { assert.True(t, c.GRPCEnabled) },
		},
		{
			name:  "GRPC_ADDR",
			env:   map[string]string{"GRPC_ADDR": ":7777"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, ":7777", c.GRPCAddr) },
		},
		{
			name:  "MONGO_URI",
			env:   map[string]string{"MONGO_URI": "mongodb://mongo:27017"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "mongodb://mongo:27017", c.MongoURI) },
		},
		{
			name:  "MONGO_DATABASE",
			env:   map[string]string{"MONGO_DATABASE": "other_db"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "other_db", c.MongoDatabase) },
		},
		{
			name:  "KAFKA_ENABLED",
			env:   map[string]string{"KAFKA_ENABLED": "true"},
			check: func(t *testing.T, c *config.Config) { assert.True(t, c.KafkaEnabled) },
		},
		{
			name:  "KAFKA_BROKERS",
			env:   map[string]string{"KAFKA_BROKERS": "b1:9092,b2:9092"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "b1:9092,b2:9092", c.KafkaBrokers) },
		},
		{
			name:  "KAFKA_TOPIC_CUSTOMERS",
			env:   map[string]string{"KAFKA_TOPIC_CUSTOMERS": "other.events"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "other.events", c.KafkaTopicCustomers) },
		},
		{
			name:  "KAFKA_DLQ_TOPIC",
			env:   map[string]string{"KAFKA_DLQ_TOPIC": "other.events.dlq"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "other.events.dlq", c.KafkaDLQTopic) },
		},
		{
			name:  "KAFKA_CONSUMER_GROUP",
			env:   map[string]string{"KAFKA_CONSUMER_GROUP": "other-group"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, "other-group", c.KafkaConsumerGroup) },
		},
		{
			name:  "KAFKA_PRODUCER_RETRIES",
			env:   map[string]string{"KAFKA_PRODUCER_RETRIES": "7"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 7, c.KafkaProducerRetries) },
		},
		{
			name:  "KAFKA_DEDUP_LIMIT",
			env:   map[string]string{"KAFKA_DEDUP_LIMIT": "250"},
			check: func(t *testing.T, c *config.Config) { assert.Equal(t, 250, c.KafkaDedupLimit) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)

			cfg, err := config.Load()
			require.NoError(t, err)

			tt.check(t, cfg)
		})
	}
}

// TestLoad_Defaults pins the values a deployment gets when it sets nothing but
// the two required variables.
func TestLoad_Defaults(t *testing.T) {
	setEnv(t, nil)

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "development", cfg.Env)
	assert.Equal(t, ":8080", cfg.Addr)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "./data/events.db", cfg.EventLogPath)
	assert.Equal(t, 24*time.Hour, cfg.JWTExpiry)
	assert.Equal(t, 4, cfg.WorkerCount)
	assert.Equal(t, 100, cfg.WorkerQueue)
	assert.Equal(t, 15*time.Second, cfg.WorkerDrainTimeout)
	assert.Equal(t, 5, cfg.OutboxInterval)
	assert.Equal(t, 50, cfg.OutboxBatch)
	assert.Equal(t, 5, cfg.OutboxMaxAttempts)
	assert.Equal(t, "localhost:4318", cfg.OTelEndpoint)
	assert.False(t, cfg.OTelEnabled)
	assert.False(t, cfg.GRPCEnabled)
	assert.False(t, cfg.KafkaEnabled)
	assert.Equal(t, 10_000, cfg.KafkaDedupLimit)
}

// TestLoad_RequiredVars covers the two keys that have no default: they are the
// reason BindEnv still exists at all, and validate() must reject their absence.
func TestLoad_RequiredVars(t *testing.T) {
	tests := []struct {
		name    string
		unset   string
		wantErr string
	}{
		{name: "missing DATABASE_URL", unset: "DATABASE_URL", wantErr: "DATABASE_URL is required"},
		{name: "missing JWT_SECRET", unset: "JWT_SECRET", wantErr: "JWT_SECRET is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, map[string]string{tt.unset: ""})

			_, err := config.Load()

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
