# gRPC — Parallel Transport Layer

> This document explains **why** the gRPC layer is designed this way, **how** the interceptor chain works, and **what happens** when `GRPC_ENABLED=false`. Read it before modifying anything in `internal/platform/grpc/` or `internal/customer/grpc.go`.

---

## The Problem

A REST API over HTTP/JSON is the right default for public-facing or browser-facing endpoints — it is human-readable, easy to test with curl, and universally supported. But HTTP/JSON has limits:

- **Schema is implicit** — the contract lives in Swagger docs, not the wire format. A caller that sends the wrong field gets no compile-time error.
- **Versioning is manual** — field addition, removal, and rename require careful discipline.
- **Internal efficiency** — JSON serialisation has measurable overhead at high RPC rates.

For service-to-service communication inside a trust boundary, gRPC solves all three:

- **Proto-first contract** — the `.proto` file is the source of truth. Both client and server generate typed code from the same file. A missing required field fails at codegen, not at runtime.
- **Binary wire format** — Protocol Buffers are compact and fast to serialise/deserialise.
- **Streaming** — server-side, client-side, and bidirectional streaming are first-class concepts.

This blueprint adds gRPC as a **parallel transport** alongside Echo. Both transports share the exact same `Service` and `QueryService`. No business logic lives in the gRPC layer.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                       cmd/api/main.go                        │
│                  (Composition root)                          │
└──────────────┬──────────────────────────┬───────────────────┘
               │                          │
   ┌───────────▼──────────┐  ┌────────────▼──────────────────┐
   │  platform/server     │  │  platform/grpc                │
   │  Echo HTTP :8080     │  │  gRPC :9090 (GRPC_ENABLED)   │
   │  JWTMiddleware       │  │  recovery + logging + auth    │
   └───────────┬──────────┘  └────────────┬──────────────────┘
               │                          │
               └──────────────┬───────────┘
                              │ both call the same
               ┌──────────────▼──────────────┐
               │        internal/customer     │
               │  Service (writes)            │
               │  QueryService (reads)        │
               │  domain.go, repository.go    │
               └─────────────────────────────┘
```

**Transport agnosticism** is the key principle: because `Service` and `QueryService` accept `context.Context` and domain types — not HTTP requests or Echo contexts — a second transport layer (gRPC) can be added without touching business logic.

---

## File Structure

```
proto/
└── customer/
    └── v1/
        └── customer.proto          ← Service definition (edit this)

gen/
└── customer/
    └── v1/
        ├── customer.pb.go          ← Generated — do not edit
        └── customer_grpc.pb.go     ← Generated — do not edit

internal/
├── customer/
│   └── grpc.go                     ← GRPCHandler: translates proto ↔ domain types
│
└── platform/
    └── grpc/
        ├── server.go               ← NewServer: wires interceptors + reflection
        └── interceptors.go         ← recovery, logging, auth (unary + streaming)
```

Generated files in `gen/` are committed so the repo builds without `protoc` installed.
Regenerate with `make proto` after editing the `.proto` file.

---

## Proto Definition

The service defines 5 unary RPCs and 1 server-side streaming RPC:

```protobuf
service CustomerService {
  rpc RegisterCustomer(RegisterCustomerRequest) returns (RegisterCustomerResponse);
  rpc GetCustomer(GetCustomerRequest) returns (GetCustomerResponse);
  rpc ListCustomers(ListCustomersRequest) returns (ListCustomersResponse);
  rpc UpdateCustomer(UpdateCustomerRequest) returns (UpdateCustomerResponse);
  rpc RemoveCustomer(RemoveCustomerRequest) returns (RemoveCustomerResponse);

  rpc WatchCustomerEvents(WatchCustomerEventsRequest) returns (stream CustomerEvent);
}
```

All date fields (`birth_date`) use ISO 8601 format (`YYYY-MM-DD`) as strings. Timestamps (`created_at`, `updated_at`, `occurred_at`) use `google.protobuf.Timestamp` for cross-language compatibility.

Regenerate after editing the proto:

```bash
make proto
# or manually:
protoc -I proto \
  --go_out=gen --go_opt=paths=source_relative \
  --go-grpc_out=gen --go-grpc_opt=paths=source_relative \
  customer/v1/customer.proto
```

---

## `GRPCHandler` — the translation layer

`internal/customer/grpc.go` implements the generated `CustomerServiceServer` interface. Its only job is translation:

```
gRPC request → parse/validate → domain command → call Service/QueryService → gRPC response
```

```go
func (h *GRPCHandler) RegisterCustomer(
    ctx context.Context,
    req *customerv1.RegisterCustomerRequest,
) (*customerv1.RegisterCustomerResponse, error) {
    birthDate, err := time.Parse("2006-01-02", req.BirthDate)
    if err != nil {
        return nil, status.Errorf(codes.InvalidArgument, "invalid birth_date format: %v", err)
    }
    id, err := h.svc.Register(ctx, RegisterCmd{...})
    if err != nil {
        return nil, mapDomainErrorToGRPC(err)
    }
    return &customerv1.RegisterCustomerResponse{Id: id.String()}, nil
}
```

**No business logic in the handler.** The `time.Parse` call is transport-level input parsing (the proto carries a string, the domain needs a `time.Time`), not domain validation. Domain validation happens inside `customer.New()` just as it does for the HTTP handler.

`GRPCHandler` accepts a `querier` interface (the same interface `Handler` uses for HTTP) so both `QueryService` and `CachedQueryService` satisfy it without any code changes.

---

## Error Mapping

Domain sentinels are mapped to gRPC status codes in `mapDomainErrorToGRPC` (in `grpc.go`):

| Domain error | gRPC code |
|---|---|
| `ErrNotFound` | `codes.NotFound` |
| `ErrEmailExists` | `codes.AlreadyExists` |
| `ErrInvalidBirthDate` | `codes.InvalidArgument` |
| `ErrNameRequired` | `codes.InvalidArgument` |
| `ErrEmailRequired` | `codes.InvalidArgument` |
| anything else | `codes.Internal` — generic message only |

**Why is the mapping in `internal/customer/grpc.go` and not in `internal/platform/grpc/`?**

`internal/platform/grpc/server.go` imports `internal/customer` (to accept `*customer.GRPCHandler`). If `customer/grpc.go` also imported `platform/grpc`, we'd have a circular import. The mapping lives in the `customer` package — the consumer of domain errors — which is where interfaces-at-the-consumer dictates it should be.

---

## Interceptors

Three interceptors are applied in order for both unary and streaming calls:

### 1. Recovery (first — always)

Catches panics in any handler and returns `codes.Internal`. Without this, a single panicking handler kills the server goroutine. The stack trace is logged at `ERROR` level.

### 2. Logging

Logs method name, gRPC status code, and duration in milliseconds for every call using `slog.InfoContext`. This gives the same structured request log you get from the Echo middleware on the HTTP side.

### 3. Auth

Reads the `Authorization: Bearer <token>` header from gRPC metadata and calls `auth.Service.ValidateToken`. On success, the JWT claims are injected into the context under `auth.ClaimsKey`. On failure, `codes.Unauthenticated` is returned.

**Why call `auth.Service.ValidateToken` instead of duplicating the JWT parsing?**

The HTTP middleware and the gRPC interceptor share the same validation logic through `ValidateToken`. If the signing key, algorithm, or claim structure ever changes, one change in `auth.Service` covers both transports.

### Ordering matters

```
Request arrives
    │
    ▼ recovery (outer — catches panics from all inner interceptors)
    │
    ▼ logging (measures time of inner handler including auth)
    │
    ▼ auth (if token invalid → codes.Unauthenticated, logged by logging interceptor)
    │
    ▼ handler
```

Recovery must be outermost — if auth or logging panicked, recovery still catches it.
Logging wraps auth so that failed auth attempts are logged with the correct code.

---

## `WatchCustomerEvents` — Server-Side Streaming

```go
func (h *GRPCHandler) WatchCustomerEvents(
    req *customerv1.WatchCustomerEventsRequest,
    stream customerv1.CustomerService_WatchCustomerEventsServer,
) error {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    var since time.Time
    for {
        select {
        case <-stream.Context().Done():
            return nil  // client disconnected — clean exit
        case <-ticker.C:
            events, err := h.eventLog.FetchSince(stream.Context(), req.AggregateId, since)
            // ... send events ...
        }
    }
}
```

The handler polls `eventLog.FetchSince` every 2 seconds and sends new events to the client.

**Design decisions:**

- **Poll, not push**: the event log is a SQLite append-only store. Polling is the simplest approach that avoids introducing a pub/sub system. Two-second intervals are a reasonable trade-off between latency and read pressure.
- **`since` cursor**: each tick advances `since` to the latest event's `OccurredAt`. The next poll fetches only newer events — no duplicates.
- **Empty `aggregate_id`**: an empty string streams all events across all aggregates. A non-empty ID filters to a single aggregate.
- **Clean disconnect**: `stream.Context().Done()` fires when the client cancels or the connection drops. The handler returns `nil` — gRPC status `OK` — because disconnection is a normal client-side event, not a server error.

`FetchSince` was added to `eventlog.Store` to support this endpoint. The existing `Append`-only SQLite implementation is extended with a `SELECT ... WHERE occurred_at > ?` query. The query uses `ORDER BY occurred_at ASC` so events are delivered in chronological order.

---

## gRPC Reflection

`reflection.Register(srv)` is called in `NewServer`. This enables service discovery without the proto file:

```bash
# List all registered services
grpcurl -plaintext localhost:9090 list

# List methods on CustomerService
grpcurl -plaintext localhost:9090 describe customer.v1.CustomerService

# Call ListCustomers (with a valid JWT)
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"supersecret"}' | jq -r '.token')

grpcurl -plaintext \
  -H "Authorization: Bearer $TOKEN" \
  localhost:9090 customer.v1.CustomerService/ListCustomers
```

Reflection is always on — it is appropriate for a development reference architecture. In production you may want to disable it (`reflection.Register` can be guarded with a config flag).

---

## Wiring in `main.go`

```go
if cfg.GRPCEnabled {
    grpcHandler := customer.NewGRPCHandler(customerSvc, customerQuery, eventLog)
    grpcSrv := grpcserver.NewServer(grpcHandler, authSvc, logger)

    lis, _ := net.Listen("tcp", cfg.GRPCAddr)
    go func() {
        logger.Info("grpc server starting", slog.String("addr", cfg.GRPCAddr))
        grpcSrv.Serve(lis)
    }()

    // ... after HTTP server shuts down:
    grpcSrv.GracefulStop()
}
```

`GracefulStop` blocks until all in-flight RPC handlers return. It is called after `server.Start` returns (HTTP has already drained) so in-flight gRPC calls can still access the database while the pool is live.

---

## Graceful Shutdown Sequence

```
1. OS SIGINT/SIGTERM → root ctx cancelled
2. Echo drains active HTTP requests (10s timeout) — server.Start returns
3. grpcSrv.GracefulStop() — waits for in-flight gRPC calls to complete
4. workerPool.Stop()
5. defer pool.Close() — Postgres connections released
```

Streaming calls (`WatchCustomerEvents`) exit via `stream.Context().Done()` when the server context is cancelled, so `GracefulStop` does not hang waiting for active streams.

---

## Configuration

| Variable | Default | Required | Description |
|---|---|---|---|
| `GRPC_ENABLED` | `false` | No | Set `true` to start the gRPC server alongside HTTP |
| `GRPC_ADDR` | `:9090` | No | gRPC server listen address |

When `GRPC_ENABLED=false` (the default): no listener is created, no port is bound, and no goroutine is started. The gRPC-related packages are still compiled into the binary but are dormant.

---

## Tests

### Unit tests (`internal/customer/grpc_test.go`)

Uses `google.golang.org/grpc/test/bufconn` — an in-process gRPC transport. No TCP port, no Docker:

```go
lis := bufconn.Listen(1 << 20)
srv := grpc.NewServer()
customerv1.RegisterCustomerServiceServer(srv, handler)
go srv.Serve(lis)

conn, _ := grpc.NewClient("passthrough://bufnet",
    grpc.WithContextDialer(...), grpc.WithTransportCredentials(insecure.NewCredentials()),
)
client := customerv1.NewCustomerServiceClient(conn)
```

| Test | Coverage |
|---|---|
| `TestGRPCHandler_RegisterCustomer` | Valid create, malformed date, future date |
| `TestGRPCHandler_RegisterCustomer_DuplicateEmail` | `codes.AlreadyExists` |
| `TestGRPCHandler_GetCustomer` | Found, not found, invalid UUID |
| `TestGRPCHandler_ListCustomers` | Returns populated list |
| `TestGRPCHandler_UpdateCustomer` | Success, not found |
| `TestGRPCHandler_RemoveCustomer` | Success, not found after removal |

### Interceptor tests (`internal/platform/grpc/interceptors_test.go`)

Tests the full interceptor chain (recovery + logging + auth) via `grpcserver.NewServer`:

| Test | Coverage |
|---|---|
| `TestAuthInterceptor_MissingToken` | No `Authorization` header → `codes.Unauthenticated` |
| `TestAuthInterceptor_InvalidToken` | Tampered JWT → `codes.Unauthenticated` |
| `TestAuthInterceptor_ValidToken` | Valid JWT → `codes.OK` |

---

## What NOT to Do

| Do not | Why |
|---|---|
| Put business logic in `GRPCHandler` | It is a translation layer only — logic lives in `Service` and domain |
| Import `platform/grpc` from `customer/grpc.go` | Creates a circular import; error mapping lives in `customer` |
| Duplicate JWT validation in the interceptor | `auth.Service.ValidateToken` is the single source of truth |
| Use `stream.Context()` after the stream returns | The context is cancelled when the stream closes |
| Expose raw internal errors via `codes.Internal` | Always use a generic message for unexpected errors |
| Parse the proto timestamp manually | Use `timestamppb.New(t)` and `t.AsTime()` |
