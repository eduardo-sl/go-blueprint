# Redis Cache — Cache-Aside on the Read Path

> This document explains **why** the cache is designed this way, **how** it works internally, and **what happens** when it fails. Read it before modifying anything in `internal/platform/cache/` or `internal/customer/query.go`.

---

## The Problem

The customer read path always hits Postgres. For most workloads that is fine, but as load grows two problems emerge:

1. **Read latency**: every round-trip to Postgres adds network + disk I/O time.
2. **Connection pool exhaustion**: `pgxpool` has a fixed number of connections. With many concurrent readers, the pool is saturated before the database is.

The classic fix is a cache in front of the database. The question is: *which caching pattern*?

---

## Why Cache-Aside (Not Write-Through or Read-Through)

Three classic patterns:

```
┌──────────────────────────────────────────────────────────┐
│  WRITE-THROUGH                                           │
│  App → Cache → DB  (same write operation)               │
│                                                          │
│  Problem: any cache failure blocks the write.            │
│  Cache becomes a critical dependency of the write path.  │
└──────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────┐
│  READ-THROUGH                                            │
│  App → Cache → (miss → Cache fetches from DB)           │
│                                                          │
│  Problem: miss logic lives inside the infrastructure.    │
│  Hard to audit, test, or debug.                          │
└──────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────┐
│  CACHE-ASIDE  ← chosen here                             │
│  App → Cache (hit → return)                             │
│          ↓ miss                                          │
│         DB → App → Cache (populate)                     │
│                                                          │
│  Miss logic lives in application code.                  │
│  Explicit, testable, auditable. Cache can fail           │
│  without affecting the write path.                       │
└──────────────────────────────────────────────────────────┘
```

**Cache-aside has a trade-off**: on the first read after a write, data may be stale until the TTL expires or the service explicitly invalidates the key. That is why explicit invalidation on the write path is mandatory — covered below.

---

## Layered Architecture

```
internal/
├── platform/cache/
│   ├── cache.go       → Cache interface + NoopCache
│   └── redis.go       → RedisCache (concrete implementation)
│
└── customer/
    └── query.go       → QueryService + CachedQueryService (wrapper)
```

The domain (`internal/customer/`) never imports Redis directly. It only knows the `Cache` interface.

---

## The `Cache` Interface

```go
// internal/platform/cache/cache.go

type Cache interface {
    Get(ctx context.Context, key string) ([]byte, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Ping(ctx context.Context) error
}

var ErrCacheMiss = errors.New("cache miss")
```

**Why `[]byte` instead of `any`?**

`[]byte` is the most honest contract. The cache stores bytes — the caller is responsible for serialization and deserialization. This means:

- Serialization is explicit (JSON, proto, etc.) — no magic.
- Replacing Redis with Memcached or an in-process cache does not change the interface.
- Unit tests can inject arbitrary bytes to test corruption scenarios.

**Why `ErrCacheMiss` as a sentinel instead of `(nil, nil)`?**

`(nil, nil)` is ambiguous: does the key not exist, or is it stored as an empty value? A sentinel forces the caller to make an explicit decision via `errors.Is`. You cannot accidentally ignore a cache miss.

---

## `NoopCache` — The Zero-Value Safe Fallback

```go
// internal/platform/cache/cache.go

type NoopCache struct{}

func (NoopCache) Get(_ context.Context, _ string) ([]byte, error) {
    return nil, ErrCacheMiss  // always a miss
}
func (NoopCache) Set(...) error    { return nil }  // silently discards
func (NoopCache) Delete(...) error { return nil }
func (NoopCache) Ping(...) error   { return nil }  // never fails
```

`NoopCache` exists for two reasons:

1. **Startup without Redis**: when `REDIS_ADDR` is not configured, the system starts with `NoopCache`. No connection failure, no panic, no 5xx. The system remains correct — just without the performance benefit.

2. **Unit tests**: any test that does not want to exercise cache behavior can use `cache.NoopCache{}` without a running Redis server.

The cache variable in `main.go` starts as `NoopCache` and is only replaced by `RedisCache` if the initial ping succeeds:

```go
// cmd/api/main.go

var customerCache cache.Cache = cache.NoopCache{}  // safe default
if cfg.RedisAddr != "" {
    rc, err := cache.NewRedisCache(...)
    if err != nil {
        logger.Warn("redis unavailable, cache disabled", ...)
        // NoopCache remains — no panic, no exit
    } else {
        customerCache = rc
    }
}
```

---

## `RedisCache` — The Implementation

```go
// internal/platform/cache/redis.go

type RedisCache struct {
    client *redis.Client
    logger *slog.Logger
}
```

### Connection and Timeouts

```go
client := redis.NewClient(&redis.Options{
    Addr:         addr,
    Password:     password,
    DB:           db,
    DialTimeout:  2 * time.Second,
    ReadTimeout:  1 * time.Second,
    WriteTimeout: 1 * time.Second,
})
```

The timeouts are intentionally conservative. Cache is an optimization — if Redis is slow, it is better to do a Postgres round-trip than to hold the request for seconds waiting on a cache hit.

### Startup Ping

```go
func NewRedisCache(...) (*RedisCache, error) {
    // ...
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    if err := client.Ping(ctx).Err(); err != nil {
        _ = client.Close()
        return nil, fmt.Errorf("cache.NewRedisCache: ping failed: %w", err)
    }
    // ...
}
```

The ping on startup makes the process **fail fast** if Redis is configured but unreachable. If `REDIS_ADDR` is set but Redis does not respond, the caller receives an error and `main.go` falls back to `NoopCache`. No surprise in production.

### Mapping `redis.Nil` → `ErrCacheMiss`

```go
func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
    val, err := r.client.Get(ctx, key).Bytes()
    if errors.Is(err, redis.Nil) {
        return nil, ErrCacheMiss  // translates go-redis sentinel to our contract
    }
    if err != nil {
        return nil, fmt.Errorf("cache.Get %q: %w", key, err)
    }
    return val, nil
}
```

`redis.Nil` is the `go-redis` sentinel for "key does not exist". `RedisCache` translates it to `ErrCacheMiss` at the boundary — no other code in the system needs to import `go-redis` to understand the error. This is the simplest possible **anti-corruption layer**.

---

## `CachedQueryService` — Composition, Not Modification

The original `QueryService` was not modified:

```go
// internal/customer/query.go

type QueryService struct {
    repo Repository
}

func (q *QueryService) GetByID(ctx context.Context, id uuid.UUID) (Customer, error) { ... }
func (q *QueryService) List(ctx context.Context) ([]Customer, error) { ... }
```

Instead, a wrapper was added in the same file:

```go
type CachedQueryService struct {
    base    *QueryService   // database access
    cache   cache.Cache     // cache layer
    ttl     time.Duration   // TTL for individual records
    listTTL time.Duration   // shorter TTL for list results
    logger  *slog.Logger
}
```

**Why a wrapper instead of modifying `QueryService`?**

Two reasons:

1. **Single responsibility**: `QueryService` knows how to fetch data. Caching is a cross-cutting concern — it does not belong to the object that runs queries.

2. **Testability**: domain unit tests (`customer_test.go`) use `QueryService` directly without any cache. Cache tests use `CachedQueryService` with a mock cache. No entanglement.

### `GetByID` Control Flow

```
GetByID(ctx, id)
       │
       ▼
  cache.Get(key)
       │
   ┌───┴─────────────────────────────────────────────┐
   │  hit?         miss?          connection error?  │
   ▼              ▼                    ▼             │
return        DB.FindByID()       WarnContext()       │
 value             │             + DB.FindByID()     │
                   ▼                  │              │
              cache.Set()             │              │
                   │                  │              │
                   └──────────────────┘              │
                            │                        │
                            ▼                        │
                       return value                  │
                                                     └─┘
```

In code:

```go
func (c *CachedQueryService) GetByID(ctx context.Context, id uuid.UUID) (Customer, error) {
    key := cacheKeyPrefix + id.String()

    // 1. Try cache
    if data, err := c.cache.Get(ctx, key); err == nil {
        var customer Customer
        if jsonErr := json.Unmarshal(data, &customer); jsonErr == nil {
            return customer, nil  // clean hit
        }
        // Corrupted entry — evict and fall through to DB
        _ = c.cache.Delete(ctx, key)
    } else if !errors.Is(err, cache.ErrCacheMiss) {
        // Redis problem — log but do not return error to client
        c.logger.WarnContext(ctx, "cache.Get failed, degrading to DB", ...)
    }

    // 2. DB (miss or Redis down)
    customer, err := c.base.GetByID(ctx, id)
    if err != nil {
        return Customer{}, err
    }

    // 3. Populate cache (best-effort — failure is not fatal)
    if data, jsonErr := json.Marshal(customer); jsonErr == nil {
        if setErr := c.cache.Set(ctx, key, data, c.ttl); setErr != nil {
            c.logger.WarnContext(ctx, "cache.Set failed", ...)  // log, do not return error
        }
    }

    return customer, nil
}
```

**Critical points:**

- **Corrupted entry**: if JSON does not deserialize, the key is evicted and the DB is queried. The cache never serves invalid data.
- **Redis down on Get**: `WarnContext` + DB fallback. The client receives correct data. No 5xx.
- **Redis down on Set**: `WarnContext` + data returned. Cache population fails silently — next call goes to DB again. Correct.
- **DB error**: propagated to the caller. The cache never hides real errors.

---

## Cache Invalidation — Explicit Writes

The write service (`Service`) receives the `Cache` instance **solely for invalidation**. It never reads from it:

```go
// internal/customer/service.go

type Service struct {
    repo        Repository
    db          Beginner
    outboxStore outbox.OutboxStore
    eventLog    eventlog.Store
    cache       cache.Cache   // ← for invalidation only
    logger      *slog.Logger
}
```

After each successful write, all affected keys are deleted:

```go
func (s *Service) invalidate(ctx context.Context, id uuid.UUID) {
    keys := []string{
        cacheKeyPrefix + id.String(),  // "customer:<uuid>"
        cacheKeyList,                  // "customer:list"
    }
    for _, key := range keys {
        if err := s.cache.Delete(ctx, key); err != nil {
            s.logger.WarnContext(ctx, "cache invalidation failed", ...)
            // do not return error — stale cache beats a failed write
        }
    }
}
```

**Why delete instead of update-on-write?**

Updating the cache on write is tempting but problematic under concurrency:

```
Thread A: writes customer X → updates cache[X] = new value
Thread B: reads customer X  → cache hit → returns old value
                               (Thread B read before A's write arrived)

Result: A's write is invisible to B through the cache.
```

Delete is safe: on the next read the cache is empty and the DB is queried. The correct value always arrives.

---

## Key Convention

| Key | TTL | Invalidated on |
|-----|-----|----------------|
| `customer:<uuid>` | `CACHE_TTL` (default 5m) | `Update`, `Remove` |
| `customer:list` | `max(TTL/5, 1m)` | `Register`, `Update`, `Remove` |

`listTTL` is calculated as one-fifth of the individual TTL, with a minimum of one minute:

```go
listTTL := max(ttl/5, time.Minute)
```

The list is more volatile — any new record invalidates it. A shorter TTL reduces stale windows without hammering the database.

---

## Graceful Degradation — Failure Map

| Scenario | Behavior |
|----------|----------|
| `REDIS_ADDR` not set | `NoopCache` from startup. Zero failures. |
| Redis down at startup | Warning logged, `NoopCache` activated. Service starts normally. |
| Redis fails in production (Get error) | `WarnContext` + DB queried. Client does not notice. |
| Redis fails in production (Set error) | `WarnContext` + data returned. Next read goes to DB. |
| Corrupted JSON entry in Redis | Evicted + DB queried + cache repopulated. Self-healing. |
| DB unavailable (cache healthy) | Cache serves ongoing requests until TTL expires. |

The core principle: **cache is an optimization, not a critical dependency**. The system is correct without it — just slower.

---

## Health Check

The `/health` endpoint reports cache status without failing when it is degraded:

```go
// internal/platform/server/server.go

func healthCheck(cfg *config.Config, appCache CachePinger) echo.HandlerFunc {
    return func(c echo.Context) error {
        cacheStatus := "ok"
        if err := appCache.Ping(c.Request().Context()); err != nil {
            cacheStatus = "degraded"  // Redis down → "degraded", not an HTTP error
        }
        return c.JSON(http.StatusOK, map[string]any{
            "status": "ok",           // always 200
            "cache":  cacheStatus,
        })
    }
}
```

`CachePinger` is a local interface in the `server` package — the server does not import the `cache` package directly:

```go
type CachePinger interface {
    Ping(ctx context.Context) error
}
```

Both `RedisCache` and `NoopCache` satisfy this interface. The server does not know which one is injected.

---

## Configuration

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `REDIS_ADDR` | `""` | No | Redis address. Empty → `NoopCache`. |
| `REDIS_PASSWORD` | `""` | No | Password (empty = no auth). |
| `REDIS_DB` | `0` | No | Redis database index. |
| `CACHE_TTL` | `5m` | No | TTL for individual records. |

---

## Tests

### Unit — `internal/customer/cached_query_test.go`

Tests `CachedQueryService` with a `mockCache` (plain struct, no Docker):

| Scenario | What it verifies |
|----------|-----------------|
| Cache hit | No DB call made |
| Cache miss | DB called once, value populated in cache |
| Corrupted entry | Key evicted, DB called, cache repopulated |
| Redis Get fails | DB called, result returned without error |
| Redis Set fails | Result returned without error, no error exposed to client |
| DB fails | Error propagated correctly |

`countingRepo` wraps `mockRepo` and counts calls to verify exactly how many times the database was queried — essential for confirming cache hits.

### Integration — `internal/platform/cache/cache_integration_test.go`

Spins up a real Redis via testcontainers:

```go
//go:build integration
```

Run with `go test -tags=integration ./...`. Verifies real TTL expiry, real delete, and Ping against the server.

---

## What NOT to Do

| Do not | Why |
|--------|-----|
| Read from cache in the write path | `Service` only invalidates — never reads. Reading in the write path creates dependency cycles. |
| Return errors to the client when cache fails | Cache is non-critical. Redis errors go to the log, not the response. |
| Use zero TTL in production | Entry never expires. A bug that populates bad data persists forever. |
| Store `time.Time` without calling `.UTC()` first | `time.Time` serializes with timezone. Deserializing on a different machine may shift the value. Always use UTC before storing. |
| Ignore `ErrCacheMiss` with `_` | It is a sentinel — it must be handled explicitly with `errors.Is`. |
