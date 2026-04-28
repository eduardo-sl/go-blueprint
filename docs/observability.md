# Observability — Traces, Metrics, and Logs

> This document explains **why** the observability layer is designed this way, **how** each signal type is collected, and **what happens** when it is disabled or the backend is unreachable. Read it before modifying anything in `internal/platform/telemetry/` or the OTel wiring in `cmd/api/main.go`.

---

## The Problem

A running service produces three kinds of signals that answer different questions:

| Signal | Question it answers |
|--------|---------------------|
| **Traces** | Where did this request spend its time? Which DB query was slow? |
| **Metrics** | How many registrations per minute? What is the p99 HTTP latency? |
| **Logs** | What happened in this specific request? What error occurred? |

Each signal is useful alone. Together they become far more powerful — a log line that carries a `trace_id` can be correlated with the exact distributed trace in Jaeger. A metric spike at 14:32 can be cross-referenced with a log error at the same time.

The challenge: **wiring these three signals together without coupling every package to the observability infrastructure**.

---

## Architecture

```
internal/platform/telemetry/
├── otel.go        → Provider: Setup(), Shutdown() — initializes TracerProvider + MeterProvider
├── metrics.go     → Package-level metric instruments + InitMetrics()
├── middleware.go  → EchoMiddleware: per-request span + route attributes
├── pgxtracer.go   → PgxTracer: OTel span + metric for every SQL query
└── slog.go        → OTelHandler: injects trace_id/span_id into slog records
```

No package outside `telemetry/` imports the OTel SDK directly — except `internal/customer/` which calls `otel.Tracer()` for service-level spans. The rest of the system is unaware of the backend.

---

## Signal 1: Traces

### How spans are created

There are three instrumentation points:

```
HTTP request arrives
       │
       ▼
EchoMiddleware (telemetry.EchoMiddleware)
  └─ starts root span: "GET /api/v1/customers/:id"
       │
       ▼
customer.QueryService.GetByID (otel.Tracer("customer.query"))
  └─ starts child span: "customer.QueryService.GetByID"
       │
       ▼
PgxTracer (pgx.QueryTracer interface)
  └─ starts child span: "pgx.query"
       │
       ▼
Postgres
```

Each layer is a child of the one above. Jaeger renders this as a single trace with a waterfall of nested spans.

### `EchoMiddleware` — the HTTP boundary

```go
// internal/platform/telemetry/middleware.go

func EchoMiddleware(serviceName string) echo.MiddlewareFunc {
    base := otelecho.Middleware(serviceName)

    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return base(func(c echo.Context) error {
            span := trace.SpanFromContext(c.Request().Context())

            span.SetAttributes(
                attribute.String("http.route", c.Path()),           // route template, not raw path
                attribute.String("http.request_id", c.Response().Header().Get(echo.HeaderXRequestID)),
            )

            err := next(c)

            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
            } else if status := c.Response().Status; status >= 400 {
                span.SetStatus(codes.Error, http.StatusText(status))
            }

            return err
        })
    }
}
```

**Why `c.Path()` instead of the raw URL?**

Raw paths like `/api/v1/customers/7a9b2c3d-...` are high-cardinality — every UUID creates a unique span name. `c.Path()` returns the route template (`/api/v1/customers/:id`), which groups all requests to the same handler under a single span name. High-cardinality span names explode storage and make Jaeger unusable.

**W3C TraceContext propagation**: `otelecho.Middleware` reads incoming `traceparent`/`tracestate` headers automatically. If the caller is itself instrumented (another service, a load balancer), the trace continues without a gap.

### Service spans

```go
// internal/customer/service.go

var svcTracer = otel.Tracer("customer.service")

func (s *Service) Register(ctx context.Context, cmd RegisterCmd) (uuid.UUID, error) {
    ctx, span := svcTracer.Start(ctx, "customer.Service.Register")
    defer span.End()

    span.SetAttributes(attribute.String("customer.email", cmd.Email))

    // ... business logic ...

    span.SetAttributes(attribute.String("customer.id", c.ID.String()))
    telemetry.CustomerRegistrations.Add(ctx, 1)
    return c.ID, nil
}
```

Each error path calls both `span.RecordError(err)` and `span.SetStatus(codes.Error, ...)`. `RecordError` attaches the error as a span event (stacktrace included). `SetStatus` marks the span red in Jaeger so it appears in error filters. Both are needed — one without the other leaves incomplete data.

### `PgxTracer` — automatic SQL instrumentation

```go
// internal/platform/telemetry/pgxtracer.go

type PgxTracer struct{ tracer trace.Tracer }

func (t *PgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
    ctx, span := t.tracer.Start(ctx, "pgx.query",
        trace.WithSpanKind(trace.SpanKindClient),
        trace.WithAttributes(
            attribute.String("db.system", "postgresql"),
            attribute.String("db.statement", data.SQL),
        ),
    )
    return context.WithValue(ctx, pgxSpanKey{}, spanWithStart{span: span, start: time.Now()})
}

func (t *PgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
    val, ok := ctx.Value(pgxSpanKey{}).(spanWithStart)
    // ...
    DBQueryDuration.Record(ctx, time.Since(val.start).Seconds())
    if data.Err != nil {
        val.span.RecordError(data.Err)
        DBQueryErrors.Add(ctx, 1)
    }
    val.span.End()
}
```

`PgxTracer` satisfies the `pgx.QueryTracer` interface and is attached to `pgxpool.Config.ConnConfig.Tracer` at startup. Every SQL query — regardless of where in the codebase it is called — automatically produces a trace span and a duration metric. No per-query instrumentation is needed.

**The `start` timestamp** is stored in the context alongside the span. pgx calls `TraceQueryEnd` on the same context, so the duration is measured precisely from the moment pgx sends the query to the moment it receives the response — network + DB execution time included.

---

## Signal 2: Metrics

### Instrument registry

All metric instruments are package-level variables initialized by `InitMetrics()`:

```go
// internal/platform/telemetry/metrics.go

var (
    // HTTP
    HTTPRequestDuration metric.Float64Histogram
    HTTPRequestsTotal   metric.Int64Counter

    // Customer domain
    CustomerRegistrations metric.Int64Counter
    CustomerRemovals      metric.Int64Counter

    // Database
    DBQueryDuration metric.Float64Histogram
    DBQueryErrors   metric.Int64Counter

    // Cache
    CacheHits   metric.Int64Counter
    CacheMisses metric.Int64Counter

    // Outbox
    OutboxMessagesPublished metric.Int64Counter
    OutboxPublishFailures   metric.Int64Counter
)
```

**Why package-level variables instead of passing instruments through constructors?**

Metric instruments are stateless handles — they do not hold data, they only record observations. Passing them through every constructor would add parameters to `Service`, `QueryService`, `CachedQueryService`, and `Poller` purely for cross-cutting infrastructure. Package-level variables make sense here: they are always initialized (either to real instruments or to noop instruments), never nil after `init()` runs.

### The noop guarantee

```go
func init() {
    _ = InitMetrics() // binds noop instruments via the global noop MeterProvider
}
```

The `init()` function runs before `main()`. At that point the global `MeterProvider` is the SDK default — a noop provider. `InitMetrics()` binds all instruments to noop implementations that compile to no-ops at zero cost.

After `telemetry.Setup()` replaces the global provider, `main()` calls `InitMetrics()` again to rebind all variables to real instruments backed by Prometheus.

This two-phase initialization guarantees that **calling `CustomerRegistrations.Add(ctx, 1)` is always safe** — before OTel is set up, during tests, and when `OTEL_ENABLED=false`. There is no nil check anywhere in the codebase.

### Prometheus scrape endpoint

When `OTEL_ENABLED=true`, a separate HTTP server exposes metrics at `METRICS_ADDR` (default `:9091`):

```go
// cmd/api/main.go

if cfg.OTelEnabled {
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())
    metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux}
    go metricsSrv.ListenAndServe()
}
```

The main Echo server (`ADDR`, default `:8080`) is not affected. Prometheus scrapes `:9091/metrics` independently. Separating them prevents metrics traffic from appearing in the application request logs.

### Metric reference

| Metric name | Type | Labels | Description |
|---|---|---|---|
| `http.request.duration` | Histogram | — | HTTP request duration (s) |
| `http.requests.total` | Counter | — | Total HTTP requests |
| `customer.registrations.total` | Counter | — | Successful customer registrations |
| `customer.removals.total` | Counter | — | Successful customer removals |
| `db.query.duration` | Histogram | — | SQL query duration (s) |
| `db.query.errors.total` | Counter | — | SQL query errors |
| `cache.hits.total` | Counter | — | Redis cache hits |
| `cache.misses.total` | Counter | — | Redis cache misses |
| `outbox.messages.published.total` | Counter | — | Outbox messages published |
| `outbox.publish.failures.total` | Counter | — | Outbox publish failures |

---

## Signal 3: Logs + Trace Correlation

### `OTelHandler` — the bridge

```go
// internal/platform/telemetry/slog.go

type OTelHandler struct{ inner slog.Handler }

func (h *OTelHandler) Handle(ctx context.Context, r slog.Record) error {
    if span := trace.SpanFromContext(ctx); span.IsRecording() {
        sc := span.SpanContext()
        r.AddAttrs(
            slog.String("trace_id", sc.TraceID().String()),
            slog.String("span_id", sc.SpanID().String()),
        )
    }
    return h.inner.Handle(ctx, r)
}
```

`OTelHandler` wraps the base `slog.Handler` (JSON in production, text in development). On every log call that carries an active span in the context, it injects `trace_id` and `span_id` as structured fields.

**What this enables**: given a log line like:

```json
{"time":"2026-04-27T21:00:00Z","level":"ERROR","msg":"save failed","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","error":"..."}
```

You can take the `trace_id`, paste it into Jaeger, and see the exact span that produced this log — including the DB query that failed, its duration, and its parent HTTP span.

**When no span is active** (background jobs, startup, shutdown), `span.IsRecording()` returns false and the record is forwarded unchanged. There is no overhead and no empty fields.

### Wiring in `main.go`

```go
func newLogger(cfg *config.Config) *slog.Logger {
    var base slog.Handler
    if cfg.Env == "production" {
        base = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
    } else {
        base = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
    }
    return slog.New(telemetry.NewOTelHandler(base))
}
```

`OTelHandler` wraps whatever the base handler is. The rest of the system calls `logger.InfoContext(ctx, ...)` — the trace injection happens transparently.

---

## The `Provider` — Lifecycle

```go
// internal/platform/telemetry/otel.go

type Provider struct {
    tracerProvider *sdktrace.TracerProvider
    meterProvider  *sdkmetric.MeterProvider
    shutdown       []func(context.Context) error
}
```

`Setup()` initializes both providers and registers them as the global OTel providers:

```go
otel.SetTracerProvider(tp)    // tracer — used by otel.Tracer() calls everywhere
otel.SetMeterProvider(mp)     // meter  — used by otel.Meter() calls everywhere
otel.SetTextMapPropagator(...)// W3C TraceContext + Baggage
```

`Shutdown()` flushes both:

```go
func (p *Provider) Shutdown(ctx context.Context) error {
    var errs []error
    for _, fn := range p.shutdown {
        errs = append(errs, fn(ctx))
    }
    return errors.Join(errs...)
}
```

`errors.Join` collects all shutdown errors without short-circuiting — if the trace exporter flush fails, the meter provider still gets a chance to flush.

---

## Shutdown Sequence

The order of operations at shutdown matters. Spans created by DB queries are child spans of HTTP spans — the tracer provider must flush **before** the database pool closes.

```
1. OS signal (SIGTERM/SIGINT)
   └─→ root context cancelled

2. Echo.Shutdown() — drains active HTTP requests
   └─→ all in-flight handlers complete (including their spans and DB queries)

3. workerPool.Stop() — drains in-flight outbox jobs

4. telProv.Shutdown(shutCtx)  ← BEFORE pool.Close()
   └─→ flushes pending spans to Jaeger via OTLP/HTTP
   └─→ flushes pending metric observations to Prometheus exporter

5. metricsSrv.Shutdown() — stops the :9091 server

6. defer pool.Close() — releases Postgres connections

7. Process exits 0
```

```go
// cmd/api/main.go

// Step 4 — flush OTel before closing the database pool.
// Pending spans include DB child spans — the tracer provider must still be
// alive when they are flushed, or they are silently dropped.
if telProv != nil {
    if err := telProv.Shutdown(shutCtx); err != nil {
        logger.Error("telemetry shutdown error", slog.Any("error", err))
    }
}
```

**Why does DB close come after OTel flush?** A span for a DB query holds a reference to the span context from the HTTP request. If the pool closes first and the tracer flushes second, the child span may reference a context that no longer has a valid parent — Jaeger would show it as an orphaned root span instead of part of the request trace.

---

## Graceful Degradation — When OTel is Disabled

When `OTEL_ENABLED=false` (the default):

- `telemetry.Setup()` is never called.
- The global providers remain the SDK noop providers.
- All `otel.Tracer()` and `otel.Meter()` calls return noop implementations.
- All `span.Start()`, `span.End()`, `counter.Add()` calls are zero-cost no-ops — no allocations, no goroutines, no network traffic.
- The metrics server on `:9091` is not started.
- `telProv` is `nil` in `main.go` — the shutdown block is skipped.

The service runs identically with or without observability enabled. There are no conditional checks scattered through the business logic.

---

## Configuration

| Variable | Default | Required | Description |
|---|---|---|---|
| `OTEL_ENABLED` | `false` | No | Set `true` to activate traces and metrics |
| `OTEL_SERVICE_NAME` | `go-blueprint` | No | Service name reported to Jaeger and Prometheus |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4318` | No | OTLP/HTTP collector endpoint — **host:port only, no scheme** |
| `METRICS_ADDR` | `:9091` | No | Address for the Prometheus scrape endpoint |

**Important**: `OTEL_EXPORTER_OTLP_ENDPOINT` must be `host:port` without a scheme prefix. The `otlptracehttp` SDK adds `http://` internally when `WithInsecure()` is set. Passing `http://localhost:4318` results in the invalid URL `http://http://localhost:4318`.

### Local development with Jaeger

The `docker-compose.yml` includes a Jaeger all-in-one container:

```bash
docker compose up -d

# In .env:
OTEL_ENABLED=true
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318

go run ./cmd/api
```

Jaeger UI: [http://localhost:16686](http://localhost:16686)
Prometheus metrics: [http://localhost:9091/metrics](http://localhost:9091/metrics)

---

## Tests

### `internal/platform/telemetry/telemetry_test.go`

| Test | What it verifies |
|------|-----------------|
| `TestOTelHandler_InjectsTraceAndSpanID` | `trace_id` and `span_id` appear in JSON output when a span is active |
| `TestOTelHandler_NoopWhenNoSpan` | No `trace_id` in output when no span is in context |
| `TestOTelHandler_WithAttrs_PreservesOTelBridge` | Child handlers created via `WithAttrs` still inject trace fields |
| `TestInitMetrics_RegistersAllInstruments` | All 10 instruments are non-nil after `InitMetrics()` |

Tests use `tracetest.NewInMemoryExporter` — no Jaeger, no Docker. The in-memory exporter is the OTel SDK's own test helper and captures spans synchronously.

---

## What NOT to Do

| Do not | Why |
|--------|-----|
| Pass `http://` in `OTEL_EXPORTER_OTLP_ENDPOINT` | The SDK adds the scheme. Double prefix produces an invalid URL that fails to parse |
| Call `otel.Tracer()` or `otel.Meter()` before `Setup()` | Safe (returns noop), but the instruments bind to the noop provider. `InitMetrics()` must be called after `Setup()` to rebind to real instruments |
| Shut down the DB pool before `telProv.Shutdown()` | DB child spans are orphaned in Jaeger — they appear as disconnected root spans instead of part of the request trace |
| Use raw path (`c.Request().URL.Path`) as a span attribute | High cardinality — one entry per UUID. Use `c.Path()` (route template) |
| Add a nil check before calling metric instruments | `init()` guarantees all instruments are non-nil. Nil checks are noise |
| Log `trace_id` manually | `OTelHandler` does it automatically for any log call that carries a context with an active span |
