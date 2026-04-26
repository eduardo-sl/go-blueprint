package cache

import (
	"context"
	"errors"
	"time"
)

// Cache is a generic key-value store for serializable values.
// The interface is defined here — implementations satisfy it implicitly.
type Cache interface {
	// Get retrieves a value. Returns ErrCacheMiss if the key does not exist.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores a value with the given TTL. Zero TTL means no expiry.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete removes a key. A missing key is not an error.
	Delete(ctx context.Context, key string) error
	// Ping verifies connectivity. Used in health checks.
	Ping(ctx context.Context) error
}

// ErrCacheMiss is returned by Get when the key is not found.
// Callers must check with errors.Is(err, cache.ErrCacheMiss).
var ErrCacheMiss = errors.New("cache miss")

// NoopCache is a no-op implementation used when Redis is not configured.
// Every Get returns ErrCacheMiss; Set and Delete are silent no-ops.
type NoopCache struct{}

func (NoopCache) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, ErrCacheMiss
}

func (NoopCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

func (NoopCache) Delete(_ context.Context, _ string) error {
	return nil
}

func (NoopCache) Ping(_ context.Context) error {
	return nil
}
