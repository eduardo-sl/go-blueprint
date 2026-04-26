//go:build integration

package customer_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startTestRedis(t *testing.T) string {
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

func TestCachedQueryService_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	addr := startTestRedis(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	rc, err := cache.NewRedisCache(addr, "", 0, logger)
	require.NoError(t, err)

	repo := newMockRepo()
	c := seedCustomer(t, repo)
	cr := &countingRepo{inner: repo}

	svc := customer.NewCachedQueryService(
		customer.NewQueryService(cr),
		rc,
		5*time.Minute,
		logger,
	)

	ctx := context.Background()

	t.Run("first call hits DB and populates cache", func(t *testing.T) {
		got, err := svc.GetByID(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
		assert.Equal(t, 1, cr.getCalls)
	})

	t.Run("second call returns from cache", func(t *testing.T) {
		got, err := svc.GetByID(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
		assert.Equal(t, 1, cr.getCalls, "DB must not be called again")
	})

	t.Run("after cache eviction next call hits DB again", func(t *testing.T) {
		key := "customer:" + c.ID.String()
		require.NoError(t, rc.Delete(ctx, key))

		got, err := svc.GetByID(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)
		assert.Equal(t, 2, cr.getCalls, "DB should be called after eviction")
	})
}
