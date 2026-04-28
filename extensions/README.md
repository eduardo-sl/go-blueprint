# Go Blueprint — Extensions Index

This directory contains implementation specifications for extending the
[go-blueprint](https://github.com/eduardo-sl/go-blueprint) core.

Each spec is a self-contained instruction set for Claude Code.
Read the relevant SPEC.md and implement one extension at a time.

---

## Execution Order

Extensions have dependencies. Follow this order:

```
1. cache/SPEC.md        — Redis cache-aside (no deps beyond core)
2. worker/SPEC.md       — Worker pool + Transactional Outbox (no deps beyond core)
3. observability/SPEC.md — OpenTelemetry (no deps beyond core, but best applied before Kafka)
4. grpc/SPEC.md         — gRPC server (no deps beyond core)
5. mongodb/SPEC.md      — Product Catalog + MongoDB (no deps beyond core)
6. messaging/SPEC.md    — Kafka (REQUIRES worker/SPEC.md — Kafka replaces LogPublisher)
```

Extensions 1–5 are **independent** — they can be implemented in any order relative to each other.
Extension 6 (Kafka) **depends on** Extension 2 (Worker/Outbox).

---

## Extension Summary

| Extension     | File                        | New Dependencies                          | Bounded Context |
|---------------|-----------------------------|-------------------------------------------|-----------------|
| Cache         | `cache/SPEC.md`             | `go-redis/v9`                             | Customer        |
| Worker/Outbox | `worker/SPEC.md`            | None (stdlib only)                        | Customer        |
| Observability | `observability/SPEC.md`     | `go.opentelemetry.io/otel` + exporters    | Cross-cutting   |
| gRPC          | `grpc/SPEC.md`              | `google.golang.org/grpc`, `protoc`        | Customer        |
| MongoDB       | `mongodb/SPEC.md`           | `go.mongodb.org/mongo-driver/v2`          | Product (new)   |
| Kafka         | `messaging/SPEC.md`         | `github.com/twmb/franz-go`               | Customer        |

---

## Skills — Applied to All Extensions

```bash
npx skills add eduardo-sl/go-agent-skills
```

Baseline skills mandatory for every file in every extension:
- `go-coding-standards`
- `go-error-handling`
- `go-code-review` (before each commit)
- `git-commit` (commit messages)

---

## Architecture Constraints (apply to all extensions)

These constraints from the core blueprint do NOT relax in extensions:

1. **Interfaces at the consumer** — every new interface lives in the package that uses it,
   not in the package that implements it.

2. **No cross-context DB calls** — Customer (Postgres) and Product (MongoDB) never share
   a database operation. The handler is the only integration point between contexts.

3. **No mediator** — extensions add explicit function calls, not a dispatch table.

4. **Feature flags via env var** — every extension has an `_ENABLED` config variable.
   `false` means zero startup dependencies — no port, no connection, no failure.

5. **Graceful degradation** — infrastructure failures (Redis down, Kafka unreachable)
   must not return 5xx to clients for non-critical paths. Log and continue.

6. **Errors are values** — every error wrapped with context. No panic in business logic.
   No silent discard.
