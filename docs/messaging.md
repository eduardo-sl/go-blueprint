# Kafka Messaging

> This document explains **why** Kafka was added, **how** each piece works internally,
> and **what happens** in every failure mode. Read it before touching anything in
> `internal/platform/kafka/` or `internal/customer/events.go`.

---

## The Problem: LogPublisher Is Not Production-Ready

The transactional outbox (`internal/outbox/`) guarantees that domain events are written
atomically with domain records. But the default publisher — `LogPublisher` — simply
logs the event. It is a development stub, not a real delivery mechanism.

```
Outbox poller picks up undelivered event
└─→ LogPublisher.Publish(msg) → logs it, returns nil
    ↑ no downstream consumer ever receives the event
```

For real async integrations (notifications, search index updates, downstream services),
we need a durable message broker. Kafka provides exactly that.

---

## Architecture: Kafka as the Outbox Backend

The Kafka integration replaces `LogPublisher` as the outbox `Publisher`. The overall
flow is unchanged — only the delivery mechanism changes:

```
┌──────────────────────────────────────────────────────────────────────┐
│  DOMAIN WRITE (unchanged — one atomic transaction)                   │
│                                                                      │
│   BEGIN;                                                             │
│   INSERT INTO customers (...);     ← domain record                  │
│   INSERT INTO outbox_messages (...);  ← pending event               │
│   COMMIT;                                                            │
└──────────────────────────────────────────────────────────────────────┘

                            ↓ (async — outbox poller)

┌──────────────────────────────────────────────────────────────────────┐
│  OUTBOX POLLER                                                       │
│                                                                      │
│   SELECT ... FOR UPDATE SKIP LOCKED                                  │
│   KafkaProducer.Publish(msg)  ← was LogPublisher                    │
│     ├─ retry up to N times with exponential backoff                  │
│     ├─ success → UPDATE outbox SET processed_at = now()             │
│     └─ max retries exceeded → DLQWriter.Write(msg, err)             │
└──────────────────────────────────────────────────────────────────────┘

                            ↓ (Kafka broker)

┌──────────────────────────────────────────────────────────────────────┐
│  CONSUMER (runs in the same process, separate goroutine)             │
│                                                                      │
│   PollFetches → dispatch to customer.EventHandler                   │
│     ├─ idempotency check (message_id dedup)                          │
│     ├─ route by event_type: Registered / Updated / Removed          │
│     ├─ success → CommitUncommittedOffsets (at-least-once)           │
│     └─ failure → batch NOT committed, redelivered on next poll      │
└──────────────────────────────────────────────────────────────────────┘
```

**Design constraint**: both producer and consumer run in the same process. In a larger
system, the consumer would be a separate service. For this blueprint, co-location keeps
the deployment simple while demonstrating all the patterns.

---

## Key Design Decisions

### franz-go over confluent-kafka-go / sarama

| Library | Why not |
|---|---|
| `confluent-kafka-go` | Requires CGo — breaks cross-compilation, Docker multi-stage builds, and pure-Go tooling |
| `sarama` | Maintenance history of breaking API changes; complex configuration surface |
| **`franz-go`** | Pure Go, actively maintained, clean API, first-class kfake for testing |

### Aggregate ID as Partition Key

```go
record := &kgo.Record{
    Key:   []byte(msg.AggregateID.String()), // ← partition key
    Value: msg.Payload,
}
```

Kafka guarantees ordering within a partition. By using the aggregate ID as the key,
all events for a given customer land on the same partition, in order. A consumer
processing CustomerRegistered before CustomerUpdated for the same customer is
therefore guaranteed.

### AllISRAcks

```go
kgo.RequiredAcks(kgo.AllISRAcks())
```

`AllISRAcks` (-1) waits for the leader and all in-sync replicas to acknowledge the
write before returning. This provides the strongest durability guarantee — the record
survives a single broker failure. Slightly lower throughput than `LeaderAck`, but for
event delivery correctness, durability wins.

---

## Part 1: The Producer

### Retry with Exponential Backoff

```go
// internal/platform/kafka/producer.go

for attempt := 0; attempt <= p.retries; attempt++ {
    if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
        lastErr = err
        p.logger.WarnContext(ctx, "kafka produce failed, retrying",
            "attempt", attempt, "error", err)
        continue
    }
    return nil
}
// max retries exceeded → DLQ
return p.dlq.Write(ctx, msg, lastErr)
```

The retry loop is intentionally at the application level, separate from franz-go's
internal retry. The application-level retry controls the DLQ handoff: after `p.retries`
attempts, the message is forwarded to the dead letter topic instead of returning an error
to the outbox poller. This prevents the poller from retrying indefinitely.

### Message Headers

Every Kafka record carries three headers:

| Header | Content |
|---|---|
| `event_type` | Domain event type (e.g., `CustomerRegistered`) |
| `message_id` | UUID of the outbox message — used for consumer dedup |
| `occurred_at` | RFC3339 timestamp |

These headers let consumers process records without parsing the payload for routing.

---

## Part 2: The Dead Letter Queue (DLQ)

When the producer exhausts its retries, the message is written to the DLQ topic
(`customers.events.dlq`) instead of being lost. The DLQ record carries the original
payload plus failure metadata:

| Header | Content |
|---|---|
| `event_type` | Original event type |
| `message_id` | Original message UUID |
| `failure_reason` | Error string from the last produce attempt |
| `failed_at` | RFC3339 timestamp of the failure |

**DLQ write failure**: if the DLQ write itself fails, the error is logged as critical
but the function still returns an error (not nil). This propagates up to the outbox
poller, which marks the message as failed. The message remains in the outbox and will
be retried on the next poll. **No message is silently discarded.**

```
DLQ write fails → log "DLQ write failed — message lost" → return error
→ outbox poller marks message as failed (attempts++, last_error set)
→ next poll tick retries delivery
```

---

## Part 3: The Consumer

### At-Least-Once Delivery

```go
// internal/platform/kafka/consumer.go

var batchFailed bool
fetches.EachRecord(func(record *kgo.Record) {
    if err := c.handler.Handle(ctx, record); err != nil {
        batchFailed = true
    }
})

if !batchFailed {
    c.client.CommitUncommittedOffsets(ctx) // commit only on full success
}
```

Offsets are committed **only if the entire batch succeeded**. If any record fails,
the batch offset is not committed and the records are redelivered on the next poll.
This is at-least-once semantics — the same record may be delivered more than once.

**Consequence**: every handler must be idempotent.

### BlockRebalanceOnPoll

```go
kgo.BlockRebalanceOnPoll()
```

This prevents Kafka from triggering a group rebalance while `PollFetches` is active.
Without it, a rebalance could steal partitions from this consumer mid-batch, causing
records to be processed without their offsets being committed, leading to redelivery
of records that were actually processed.

**Critical shutdown detail**: `AllowRebalance()` must be called before any `return`
in the poll loop. If it isn't, the group management goroutine blocks indefinitely,
preventing `Close()` from completing. The implementation calls `AllowRebalance()`
immediately after every `PollFetches` return, including on context cancellation:

```go
func (c *Consumer) Run(ctx context.Context) {
    for {
        fetches := c.client.PollFetches(ctx)
        c.client.AllowRebalance() // ← always first, before any early return
        if fetches.IsClientClosed() || errors.Is(ctx.Err(), context.Canceled) {
            return
        }
        // ...
    }
}
```

---

## Part 4: Consumer Middleware

The middleware chain wraps a `Handler` with cross-cutting behavior. Applied in
`cmd/api/main.go` via `kafka.Chain(handler, mw...)`:

```go
// Example: wrap the EventHandler with logging, recovery, and idempotency
handler := kafka.Chain(
    customer.NewEventHandler(logger),
    kafka.WithLogging(logger),
    kafka.WithRecovery(logger),
    kafka.WithIdempotency(logger),
)
```

| Middleware | Behavior |
|---|---|
| `WithLogging` | Logs each record before dispatch and on error |
| `WithRecovery` | Catches panics from downstream handlers, returns them as errors |
| `WithIdempotency` | Skips records whose `message_id` header was already processed |

`WithIdempotency` uses an in-memory `sync.Map`. **It resets on process restart.** For
production dedup that survives restarts, replace the `sync.Map` with a Redis SET or a
database table with a unique constraint on `message_id`.

`EventHandler` also performs its own dedup — the middleware layer is an optional
additional safety net for when the same event reaches the process a second time
within a single run.

---

## Part 5: The EventHandler

`customer.EventHandler` is the domain-side consumer for customer events. It routes
by event type and is intentionally simple — in production it would update a read model,
trigger notifications, or call a downstream service.

```go
switch eventType {
case "CustomerRegistered": return h.handleRegistered(ctx, record.Value)
case "CustomerUpdated":    return h.handleUpdated(ctx, record.Value)
case "CustomerRemoved":    return h.handleRemoved(ctx, record.Value)
default:
    h.logger.Warn("unknown event type, skipping", "event_type", eventType)
    return nil  // ← skip, do not fail, do not block the partition
}
```

**Unknown event types are always skipped with `return nil`**. Returning an error for
an unknown type would cause the consumer to stall on every record of that type — it
would never commit the offset and would loop forever. Silently skipping is the correct
behavior for schema evolution: new event types published by a newer producer version
are invisible to older consumers.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `KAFKA_ENABLED` | `false` | Set `true` to activate. No broker connection when false. |
| `KAFKA_BROKERS` | `localhost:9092` | Comma-separated broker list |
| `KAFKA_TOPIC_CUSTOMERS` | `customers.events` | Topic for customer domain events |
| `KAFKA_DLQ_TOPIC` | `customers.events.dlq` | Dead letter topic |
| `KAFKA_CONSUMER_GROUP` | `go-blueprint` | Consumer group ID |
| `KAFKA_PRODUCER_RETRIES` | `3` | Max produce attempts before DLQ |

---

## Getting Started

### 1. Start Kafka

```bash
docker compose up -d kafka zookeeper

# Wait for Kafka to be healthy
docker compose ps
```

### 2. Create topics (optional — auto-creation is enabled)

```bash
make kafka-topics
```

### 3. Enable Kafka in .env

```bash
KAFKA_ENABLED=true
KAFKA_BROKERS=localhost:9092
```

### 4. Run the server

```bash
make run
```

Expected log on startup:
```
level=INFO msg="kafka enabled" brokers=localhost:9092 topic=customers.events group=go-blueprint
```

### 5. Produce an event via the API

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"supersecret123"}' | jq -r '.token')

curl -s -X POST localhost:8080/api/v1/customers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob","email":"bob@example.com","birth_date":"1990-01-01"}'
```

### 6. Observe the Kafka message

```bash
make kafka-consume
# or directly:
docker exec -it go-blueprint-kafka-1 \
  kafka-console-consumer --bootstrap-server localhost:9092 \
  --topic customers.events --from-beginning --property print.headers=true
```

Expected output:
```
event_type:CustomerRegistered,message_id:<uuid>,occurred_at:2026-04-28T...
{"name":"Bob","email":"bob@example.com",...}
```

---

## Failure Map

| Failure | Consequence | Recovery |
|---|---|---|
| Broker unreachable on produce | Producer retries with backoff | After N retries: DLQ write |
| DLQ write fails | Error logged, outbox marks message failed | Next outbox poll retries |
| Consumer handler returns error | Batch offset NOT committed | Same records redelivered on next poll |
| Consumer handler panics | Recovery middleware converts to error | Same as handler error above |
| Duplicate message_id | Idempotency check skips the record | No side effect |
| Unknown event_type | Handler skips with warning | No side effect, queue not blocked |
| Context cancelled | Consumer exits Run() after current poll | Close() completes cleanly |
| KAFKA_ENABLED=false | No Kafka client created | LogPublisher used instead |

---

## Testing

### Unit tests (no Docker required)

```bash
go test ./internal/platform/kafka/... -race -count=1
go test ./internal/customer/... -run TestEventHandler -race -count=1
```

Tests use `kfake` — an in-process Kafka fake from the franz-go project. No real Kafka
cluster is needed. Tests cover:
- Producer happy path (record arrives with correct key and headers)
- DLQ write (record arrives with failure metadata headers)
- Consumer happy path (handler called, offset committed)
- Consumer handler error (offset NOT committed, redelivered on next consumer)
- Context cancellation (Run exits cleanly)
- All middleware behaviors (logging, recovery, idempotency, chain order)

### Integration test (build tag: `integration`)

Spin up Kafka via testcontainers, produce a message via the outbox, assert it arrives
at the consumer:

```bash
go test ./... -tags=integration -race -count=1
```

---

## What NOT to Do

| Do not | Why |
|---|---|
| Use `sarama` or `confluent-kafka-go` | Maintenance / CGo issues — see Why franz-go above |
| Commit offsets before handler success | Breaks at-least-once guarantee |
| Return an error for unknown event types | Stalls the partition consumer indefinitely |
| Remove `AllowRebalance()` after `PollFetches` | Group management goroutine blocks, `Close()` hangs |
| Use `sync.Map` dedup in production | Resets on restart — use Redis or DB for persistent dedup |
| Skip the `KAFKA_ENABLED` gate | Startup fails without a broker; the gate is the safety net |
