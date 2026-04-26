package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopCache(t *testing.T) {
	t.Parallel()

	c := cache.NoopCache{}
	ctx := context.Background()

	t.Run("Get returns ErrCacheMiss", func(t *testing.T) {
		t.Parallel()
		_, err := c.Get(ctx, "any-key")
		require.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrCacheMiss))
	})

	t.Run("Set is a no-op", func(t *testing.T) {
		t.Parallel()
		err := c.Set(ctx, "key", []byte("val"), time.Minute)
		require.NoError(t, err)
	})

	t.Run("Delete is a no-op", func(t *testing.T) {
		t.Parallel()
		err := c.Delete(ctx, "key")
		require.NoError(t, err)
	})

	t.Run("Ping always succeeds", func(t *testing.T) {
		t.Parallel()
		err := c.Ping(ctx)
		require.NoError(t, err)
	})
}
