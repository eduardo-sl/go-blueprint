# Go Blueprint

> A reference implementation of Clean Architecture, DDD, and CQRS in idiomatic Go. Explicit, flat, and boring — the way Go is meant to be written.

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Echo](https://img.shields.io/badge/Echo-v4-blue)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-green)

---

## Overview

Go Blueprint is a production-grade reference architecture for Go REST APIs. The domain is intentionally simple — a **Customer** aggregate with register, update, and remove operations — so the focus stays on architecture, not business logic.

**What this is NOT**
- A showcase of every Go library available
- An over-engineered microservices platform
- A framework or starter kit

**What this IS**
- A production-grade reference architecture for Go REST APIs
- CQRS with explicit service types instead of a mediator bus
- Clean Architecture with Go's package model
- An append-only event log for domain audit

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    cmd/api/main.go                        │
│          (Composition root — wires everything)            │
└──────────────────┬───────────────────────────────────────┘
                   │
       ┌───────────▼────────────┐
       │   platform/server      │  Echo HTTP server
       │   platform/middleware  │  RequestID, Recover, CORS, slog
       └───────────┬────────────┘
                   │
       ┌───────────▼────────────────────────────────┐
       │            internal/customer               │
       │                                            │
       │  handler.go    ← HTTP boundary             │
       │  service.go    ← write: Register/Update/Remove │
       │  query.go      ← read:  GetByID/List       │
       │  domain.go     ← entity, validation, errors│
       │  repository.go ← interface (consumer-side) │
       └───────────┬────────────────────────────────┘
                   │ satisfies
       ┌───────────▼────────────────────────────────┐
       │   platform/database/postgres               │
       │   (sqlc-generated + repository adapters)   │
       └───────────┬────────────────────────────────┘
                   │
            ┌──────▼──────┐    ┌────────────────────┐
            │  PostgreSQL  │    │  SQLite             │
            │  (customers, │    │  (event_log —      │
            │   users)     │    │   append-only)     │
            └─────────────┘    └────────────────────┘
```

### Dependency direction

```
handlers → services → domain ← infra (postgres)
                 ↘
              eventlog (sqlite)
```

Domain has zero infrastructure imports. The `postgres` package satisfies the `customer.Repository` interface implicitly — the domain package never imports `postgres`.

### Key architectural decisions

| Decision | Reason |
|---|---|
| **No mediator/bus** | `service.Register(ctx, cmd)` is explicit, debuggable, and grep-able |
| **Interfaces at the consumer** | `customer.Repository` lives in the domain package, not in `postgres/` |
| **No ORM** | `sqlc` generates type-safe Go from SQL — the SQL is the truth |
| **Separate Write/Read services** | `Service` (writes) and `QueryService` (reads) — no side effects on reads |
| **SQLite for event log** | Lightweight append-only audit log; no Postgres dependency for events |
| **Manual wiring** | `cmd/api/main.go` wires all dependencies explicitly — no reflection, no surprises |

---

## Tech Stack

### Core

| Role | Library | Notes |
|---|---|---|
| HTTP framework | `github.com/labstack/echo/v4` | Routing, binding, grouping |
| SQL codegen | `github.com/sqlc-dev/sqlc` | Type-safe Go from `.sql` files |
| DB driver | `github.com/jackc/pgx/v5` | PostgreSQL native, fast, no CGo |
| SQLite (Event Log) | `modernc.org/sqlite` | Pure Go, no CGo |
| Migrations | `github.com/pressly/goose/v3` | SQL-first, runs at startup |

### Auth & Validation

| Role | Library |
|---|---|
| JWT | `github.com/golang-jwt/jwt/v5` — HMAC-SHA256 signed tokens |
| Validation | `github.com/go-playground/validator/v10` — struct tag based, applied in handlers |
| Password hashing | `golang.org/x/crypto/bcrypt` — cost factor 12 |

### Observability & Config

| Role | Library |
|---|---|
| Logging | `log/slog` (stdlib, Go 1.21+) — JSON in production, text in development |
| Config | `github.com/spf13/viper` + `github.com/joho/godotenv` |
| Swagger UI | `github.com/swaggo/swag` + `echo-swagger` — at `/swagger/index.html` |

### Testing

| Role | Library |
|---|---|
| Assertions | `github.com/stretchr/testify` — `assert` and `require` |
| Integration tests | `github.com/testcontainers/testcontainers-go` — real Postgres in Docker |

---

## Project Structure

```
go-blueprint/
├── cmd/
│   └── api/
│       └── main.go              # Entry point — wires everything, starts Echo
│
├── internal/
│   ├── customer/                # Customer aggregate
│   │   ├── domain.go            # Entity, sentinel errors, New(), Update()
│   │   ├── repository.go        # Repository interface (consumer-defined)
│   │   ├── service.go           # Write side: Register, Update, Remove
│   │   ├── query.go             # Read side: GetByID, List
│   │   ├── handler.go           # Echo HTTP handlers + route registration
│   │   ├── customer_test.go     # Unit tests (table-driven, in-memory mock)
│   │   └── integration_test.go  # Integration tests (build tag: integration)
│   │
│   ├── auth/
│   │   ├── domain.go            # User entity, sentinel errors
│   │   ├── repository.go        # Repository interface
│   │   ├── service.go           # Register, Login, JWT issuance
│   │   ├── handler.go           # Echo HTTP handlers
│   │   └── middleware.go        # JWT middleware (Echo MiddlewareFunc)
│   │
│   ├── eventlog/
│   │   ├── store.go             # Store interface + Event type
│   │   └── sqlite.go            # SQLite implementation
│   │
│   └── platform/
│       ├── config/config.go     # Viper config, Load(), startup validation
│       ├── database/
│       │   ├── postgres.go      # pgxpool init with connection limits
│       │   └── postgres/        # sqlc-generated code + repository adapters
│       │       ├── models.go          # Generated DB types
│       │       ├── customers.sql.go   # Generated customer queries
│       │       ├── users.sql.go       # Generated user queries
│       │       ├── customer_repo.go   # Adapter: pgtype ↔ domain time.Time
│       │       └── user_repo.go       # Adapter: pgtype ↔ domain time.Time
│       ├── middleware/          # Echo middlewares (RequestID, slog, CORS, Recover)
│       └── server/server.go     # Echo setup, route registration, graceful shutdown
│
├── migrations/                  # goose SQL migration files
│   ├── 001_create_customers.sql
│   ├── 002_create_users.sql
│   └── 003_create_event_log.sql
├── queries/                     # Raw SQL consumed by sqlc
│   ├── customers.sql
│   └── users.sql
├── docs/                        # Generated by swag init (do not edit)
├── docker-compose.yml           # PostgreSQL 16 for local development
├── .env.example                 # Environment variable template
├── .golangci.yml                # Linter configuration
├── Makefile                     # Common development tasks
└── sqlc.yaml                    # sqlc configuration
```

---

## Getting Started

### Prerequisites

- [Go 1.26+](https://go.dev/dl/)
- [Docker + Docker Compose](https://docs.docker.com/get-docker/)
- `make` (optional but recommended)

### 1. Clone and configure

```bash
git clone https://github.com/eduardo-sl/go-blueprint.git
cd go-blueprint

cp .env.example .env
```

Edit `.env` and set a strong `JWT_SECRET` (32+ characters). `DATABASE_URL` points to the local Postgres started in the next step.

### 2. Start PostgreSQL

```bash
docker compose up -d

# Confirm it is healthy before proceeding
docker compose ps
```

### 3. Run the server

```bash
# Migrations run automatically at startup via goose
go run ./cmd/api

# Or with make
make run
```

Expected output:

```
time=2026-04-26T12:00:00Z level=INFO msg="server starting" addr=:8080
```

### 4. Verify the server

```bash
curl http://localhost:8080/health
```

```json
{
  "status": "ok",
  "version": "1.0.0",
  "uptime": "1.2s",
  "env": "development"
}
```

The interactive Swagger UI is available at: [http://localhost:8080/swagger/index.html](http://localhost:8080/swagger/index.html)

---

## Validating the Basic Flows

All examples use `curl` + `jq`. You can also use the Swagger UI.

### Auth — Register and Login

**Register a user**

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","name":"Alice","password":"supersecret123"}' | jq
```

Response (`201 Created`):
```json
{ "id": "550e8400-e29b-41d4-a716-446655440000" }
```

**Login and capture token**

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"supersecret123"}' | jq -r '.token')

echo $TOKEN
```

Response (`200 OK`):
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2026-04-27T12:00:00Z"
}
```

---

### Customer CRUD

All customer endpoints require `Authorization: Bearer <token>`.

**Register a customer**

```bash
CUSTOMER_ID=$(curl -s -X POST http://localhost:8080/api/v1/customers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob Silva","email":"bob@example.com","birth_date":"1990-06-15"}' \
  | jq -r '.id')

echo $CUSTOMER_ID
```

**Get by ID**

```bash
curl -s http://localhost:8080/api/v1/customers/$CUSTOMER_ID \
  -H "Authorization: Bearer $TOKEN" | jq
```

Response (`200 OK`):
```json
{
  "id": "7a9b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d",
  "name": "Bob Silva",
  "email": "bob@example.com",
  "birth_date": "1990-06-15",
  "created_at": "2026-04-26T12:00:00Z",
  "updated_at": "2026-04-26T12:00:00Z"
}
```

**List all customers**

```bash
curl -s http://localhost:8080/api/v1/customers \
  -H "Authorization: Bearer $TOKEN" | jq
```

**Update a customer**

```bash
curl -s -X PUT http://localhost:8080/api/v1/customers/$CUSTOMER_ID \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob Silva Jr.","email":"bob.jr@example.com","birth_date":"1990-06-15"}'

# Expected: 204 No Content
```

**Remove a customer**

```bash
curl -s -X DELETE http://localhost:8080/api/v1/customers/$CUSTOMER_ID \
  -H "Authorization: Bearer $TOKEN"

# Expected: 204 No Content
```

---

### Validating Error Handling

| Scenario | Request | Expected |
|---|---|---|
| No token | `GET /api/v1/customers` (no header) | `401` `missing authorization header` |
| Wrong password | `POST /auth/login` with wrong password | `401` `invalid password` |
| Invalid email format | `POST /customers` with `"email":"not-email"` | `422` validation error |
| Future birth date | `POST /customers` with `"birth_date":"2099-01-01"` | `422` `birth date cannot be in the future` |
| Duplicate email | `POST /customers` same email twice | `409` `email already registered` |
| Customer not found | `GET /customers/<random-uuid>` | `404` `customer not found` |

Example — duplicate email:
```bash
# First registration succeeds
curl -s -X POST http://localhost:8080/api/v1/customers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob","email":"dup@example.com","birth_date":"1990-01-01"}' | jq

# Second registration with the same email returns 409
curl -s -X POST http://localhost:8080/api/v1/customers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"dup@example.com","birth_date":"1985-03-20"}' | jq
```

```json
{ "message": "email already registered" }
```

---

## API Reference

### Auth

| Method | Path | Body | Response |
|---|---|---|---|
| `POST` | `/api/v1/auth/register` | `{email, name, password}` | `201 {id}` |
| `POST` | `/api/v1/auth/login` | `{email, password}` | `200 {token, expires_at}` |

### Customers (JWT required)

| Method | Path | Body | Response |
|---|---|---|---|
| `POST` | `/api/v1/customers` | `{name, email, birth_date}` | `201 {id}` |
| `GET` | `/api/v1/customers` | — | `200 [{customer}]` |
| `GET` | `/api/v1/customers/:id` | — | `200 {customer}` |
| `PUT` | `/api/v1/customers/:id` | `{name, email, birth_date}` | `204` |
| `DELETE` | `/api/v1/customers/:id` | — | `204` |

### System

| Method | Path | Response |
|---|---|---|
| `GET` | `/health` | `200 {status, version, uptime, env}` |
| `GET` | `/swagger/*` | Swagger UI |

**birth_date format**: `YYYY-MM-DD`

All error responses:
```json
{ "message": "human readable description" }
```

---

## Running Tests

### Unit tests

```bash
go test ./... -race -count=1

# or
make test
```

Tests cover:
- Domain validation (`New`, `Update`) with table-driven cases
- Service layer (`Register`, `Remove`) with an in-memory mock repository

### Integration tests

Requires Docker. Uses testcontainers-go to spin up a real `postgres:16-alpine` instance:

```bash
go test ./... -tags=integration -race -count=1

# or
make test-integration
```

Integration tests cover the full stack from service → repository → real database:
register, update, remove, list, duplicate email, not-found.

### Linting

```bash
golangci-lint run ./...

# or
make lint
```

---

## Makefile Reference

| Target | Command | Description |
|---|---|---|
| `run` | `go run ./cmd/api` | Start the server |
| `build` | `go build -o bin/blueprint ./cmd/api` | Compile binary |
| `test` | `go test ./... -race -count=1` | Unit tests |
| `test-integration` | `go test -tags=integration -race -count=1` | Integration tests |
| `lint` | `golangci-lint run ./...` | Run linter |
| `generate` | `sqlc generate && swag init` | Regenerate sqlc + swagger |
| `migrate` | `goose up` | Apply pending migrations |
| `migrate-down` | `goose down` | Roll back last migration |
| `docker-up` | `docker compose up -d` | Start Postgres |
| `docker-down` | `docker compose down` | Stop Postgres |

---

## Environment Variables

Copy `.env.example` to `.env`:

| Variable | Default | Required | Description |
|---|---|---|---|
| `ENV` | `development` | No | `development` → text logs; `production` → JSON logs |
| `ADDR` | `:8080` | No | Server listen address |
| `DATABASE_URL` | — | **Yes** | PostgreSQL DSN (`postgres://user:pass@host/db?sslmode=disable`) |
| `EVENT_LOG_PATH` | `./data/events.db` | No | SQLite path for the event audit log |
| `JWT_SECRET` | — | **Yes** | HMAC-SHA256 signing key (32+ characters recommended) |
| `JWT_EXPIRY` | `24h` | No | Token lifetime (Go duration string: `1h`, `24h`, `7d`) |
| `LOG_LEVEL` | `info` | No | `debug` / `info` / `warn` / `error` |

---

## CQRS

Write and read operations use dedicated service types with explicit function calls:

```go
// Write path — registers the customer and appends to the event log
id, err := customerSvc.Register(ctx, customer.RegisterCmd{
    Name:      name,
    Email:     email,
    BirthDate: birthDate,
})

// Read path — no side effects, no event log, no locks
c, err := customerQuery.GetByID(ctx, id)
```

`Service` handles state-changing commands (Register, Update, Remove) and writes to the event log. `QueryService` handles reads and has no side effects. Neither type is aware of the other.

---

## Event Log

Every state-changing operation appends an event to an SQLite-backed audit log at `data/events.db`:

| Event type | Triggered by |
|---|---|
| `CustomerRegistered` | `Service.Register` |
| `CustomerUpdated` | `Service.Update` |
| `CustomerRemoved` | `Service.Remove` |

The event log is intentionally simple — append-only, no replay. It serves as an audit trail. Event writes are fire-and-forget: a failure to append does not roll back the domain operation (logged as an error instead).

---

## License

MIT
