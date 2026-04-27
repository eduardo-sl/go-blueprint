package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var queryTracer = otel.Tracer("customer.query")

type QueryService struct {
	repo Repository
}

func NewQueryService(repo Repository) *QueryService {
	return &QueryService{repo: repo}
}

func (q *QueryService) GetByID(ctx context.Context, id uuid.UUID) (Customer, error) {
	ctx, span := queryTracer.Start(ctx, "customer.QueryService.GetByID")
	defer span.End()

	span.SetAttributes(attribute.String("customer.id", id.String()))

	c, err := q.repo.FindByID(ctx, id)
	if err != nil {
		err = fmt.Errorf("customer.QueryService.GetByID: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return Customer{}, err
	}
	return c, nil
}

func (q *QueryService) List(ctx context.Context) ([]Customer, error) {
	ctx, span := queryTracer.Start(ctx, "customer.QueryService.List")
	defer span.End()

	cs, err := q.repo.List(ctx)
	if err != nil {
		err = fmt.Errorf("customer.QueryService.List: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	span.SetAttributes(attribute.Int("customer.count", len(cs)))
	return cs, nil
}

const (
	cacheKeyPrefix = "customer:"
	cacheKeyList   = "customer:list"
)

// CachedQueryService wraps QueryService with a cache-aside layer.
// On any cache error it degrades transparently to the underlying QueryService.
type CachedQueryService struct {
	base    *QueryService
	cache   cache.Cache
	ttl     time.Duration
	listTTL time.Duration
	logger  *slog.Logger
}

func NewCachedQueryService(base *QueryService, c cache.Cache, ttl time.Duration, logger *slog.Logger) *CachedQueryService {
	listTTL := max(ttl/5, time.Minute)
	return &CachedQueryService{
		base:    base,
		cache:   c,
		ttl:     ttl,
		listTTL: listTTL,
		logger:  logger,
	}
}

func (c *CachedQueryService) GetByID(ctx context.Context, id uuid.UUID) (Customer, error) {
	key := cacheKeyPrefix + id.String()

	if data, err := c.cache.Get(ctx, key); err == nil {
		var customer Customer
		if jsonErr := json.Unmarshal(data, &customer); jsonErr == nil {
			return customer, nil
		}
		// Corrupted entry — evict and fall through to DB.
		_ = c.cache.Delete(ctx, key)
	} else if !errors.Is(err, cache.ErrCacheMiss) {
		c.logger.WarnContext(ctx, "cache.Get failed, degrading to DB",
			slog.String("key", key), slog.Any("error", err))
	}

	customer, err := c.base.GetByID(ctx, id)
	if err != nil {
		return Customer{}, err
	}

	if data, jsonErr := json.Marshal(customer); jsonErr == nil {
		if setErr := c.cache.Set(ctx, key, data, c.ttl); setErr != nil {
			c.logger.WarnContext(ctx, "cache.Set failed",
				slog.String("key", key), slog.Any("error", setErr))
		}
	}

	return customer, nil
}

func (c *CachedQueryService) List(ctx context.Context) ([]Customer, error) {
	if data, err := c.cache.Get(ctx, cacheKeyList); err == nil {
		var customers []Customer
		if jsonErr := json.Unmarshal(data, &customers); jsonErr == nil {
			return customers, nil
		}
		_ = c.cache.Delete(ctx, cacheKeyList)
	} else if !errors.Is(err, cache.ErrCacheMiss) {
		c.logger.WarnContext(ctx, "cache.Get failed, degrading to DB",
			slog.String("key", cacheKeyList), slog.Any("error", err))
	}

	customers, err := c.base.List(ctx)
	if err != nil {
		return nil, err
	}

	if data, jsonErr := json.Marshal(customers); jsonErr == nil {
		if setErr := c.cache.Set(ctx, cacheKeyList, data, c.listTTL); setErr != nil {
			c.logger.WarnContext(ctx, "cache.Set failed",
				slog.String("key", cacheKeyList), slog.Any("error", setErr))
		}
	}

	return customers, nil
}
