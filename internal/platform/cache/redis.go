package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache implements Cache using go-redis/v9.
type RedisCache struct {
	client *redis.Client
	logger *slog.Logger
}

// NewRedisCache creates a RedisCache and verifies connectivity at startup.
// Returns an error if the initial ping fails — callers should fall back to NoopCache.
func NewRedisCache(addr, password string, db int, logger *slog.Logger) (*RedisCache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("cache.NewRedisCache: ping failed: %w", err)
	}

	logger.Info("redis cache connected", slog.String("addr", addr))
	return &RedisCache{client: client, logger: logger}, nil
}

func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, fmt.Errorf("cache.Get %q: %w", key, err)
	}
	return val, nil
}

func (r *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache.Set %q: %w", key, err)
	}
	return nil
}

func (r *RedisCache) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache.Delete %q: %w", key, err)
	}
	return nil
}

func (r *RedisCache) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache.Ping: %w", err)
	}
	return nil
}

// Close releases the underlying connection pool.
func (r *RedisCache) Close() error {
	return r.client.Close()
}
