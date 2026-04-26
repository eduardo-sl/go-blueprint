//go:build integration

package cache_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}
	redisC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = redisC.Terminate(ctx) })

	endpoint, err := redisC.Endpoint(ctx, "")
	require.NoError(t, err)
	return endpoint
}

func TestRedisCache_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	addr := startRedis(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	rc, err := cache.NewRedisCache(addr, "", 0, logger)
	require.NoError(t, err)

	ctx := context.Background()
	key := "test:key"
	value := []byte(`{"hello":"world"}`)

	t.Run("Set then Get", func(t *testing.T) {
		require.NoError(t, rc.Set(ctx, key, value, time.Minute))
		got, err := rc.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, got)
	})

	t.Run("Get missing key returns ErrCacheMiss", func(t *testing.T) {
		_, err := rc.Get(ctx, "does-not-exist")
		require.Error(t, err)
		assert.ErrorIs(t, err, cache.ErrCacheMiss)
	})

	t.Run("Delete removes key", func(t *testing.T) {
		require.NoError(t, rc.Set(ctx, key, value, time.Minute))
		require.NoError(t, rc.Delete(ctx, key))
		_, err := rc.Get(ctx, key)
		assert.ErrorIs(t, err, cache.ErrCacheMiss)
	})

	t.Run("TTL expires entry", func(t *testing.T) {
		shortKey := "test:short-ttl"
		require.NoError(t, rc.Set(ctx, shortKey, value, 50*time.Millisecond))
		time.Sleep(100 * time.Millisecond)
		_, err := rc.Get(ctx, shortKey)
		assert.ErrorIs(t, err, cache.ErrCacheMiss)
	})

	t.Run("Ping succeeds", func(t *testing.T) {
		require.NoError(t, rc.Ping(ctx))
	})
}
