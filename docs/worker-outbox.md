# Worker Pool + Transactional Outbox

> This document explains **why** these two patterns were implemented together, **how** each piece works internally, and **what happens** in every failure mode. Read it before touching anything in `internal/worker/`, `internal/outbox/`, or the write path in `internal/customer/service.go`.

---

## The Problem: "I Wrote to the DB, but the Event Vanished"

The system needs to notify external consumers when a customer is registered, updated, or removed. The naive approach is to publish the event after saving to the database:

```go
// WRONG — do not do this
func (s *Service) Register(ctx context.Context, cmd RegisterCmd) (uuid.UUID, error) {
    s.repo.Save(ctx, customer)    // 1. persist to DB
    publisher.Publish(ctx, event) // 2. publish event
    return customer.ID, nil
}
```

(This is illustrative — `customer.Repository` deliberately has no non-transactional
`Save`, precisely so this shape cannot be written.)

**The problem**: between step 1 and step 2 the process can die. The DB was updated. The event was never delivered. The consumer will never know the customer exists.

```
Database:  customer X created ✓
Messaging: CustomerRegistered for X → LOST
Consumer:  never knew about X
```

This is not theoretical — deploys, OOM kills, and network timeouts happen. In distributed systems, anything that can fail eventually will.

---

## The Solution: Transactional Outbox

The **Transactional Outbox** pattern reframes the question: instead of "how do we publish events reliably?", we ask "how do we guarantee the event is recorded atomically with the domain write?".

The answer: write the event to an `outbox_messages` table **in the same transaction** as the domain record. A separate process (the *poller*) reads that table and delivers the events.

```
┌──────────────────────────────────────────────────────────────────────┐
│  DOMAIN WRITE (one atomic transaction)                               │
│                                                                      │
│   BEGIN;                                                             │
│   INSERT INTO customers (...) VALUES (...);  ← domain record        │
│   INSERT INTO outbox_messages (...);         ← pending event         │
│   COMMIT;                                                            │
│                                                                      │
│   If the process dies now: both rows exist, or neither does.         │
│   There is no inconsistent state.                                    │
└──────────────────────────────────────────────────────────────────────┘

                            ↓ (asynchronous)

┌──────────────────────────────────────────────────────────────────────┐
│  POLLER (separate goroutine)                                         │
│                                                                      │
│   Every N seconds:                                                   │
│   1. SELECT ... FROM outbox_messages WHERE processed_at IS NULL      │
│      FOR UPDATE SKIP LOCKED                                          │
│   2. For each message: Publisher.Publish(msg)                        │
│   3. On success: UPDATE outbox_messages SET processed_at = now()     │
│   4. On failure: UPDATE outbox_messages SET attempts++, last_error   │
└──────────────────────────────────────────────────────────────────────┘
```

**Accepted trade-off**: events are not delivered instantly — there is a delay of up to `OUTBOX_INTERVAL` seconds. For most async integrations that is acceptable and far better than losing events.

---

## Part 1: The Worker Pool

### Why a Pool Instead of `go func()` per Job?

```go
// NAIVE — do not use in production
go publisher.Publish(ctx, msg)
```

Problems:
- Goroutines are cheap but not free. Under load, thousands of concurrent goroutines cause GC pressure.
- Without a concurrency limit, a burst of outbox messages creates a burst of goroutines.
- No backpressure — the poller has no way to know if it is overwhelming the publisher.

A pool solves this: it defines a fixed number of workers and a bounded job queue. If the queue fills up, new jobs are rejected immediately (non-blocking).

### Implementation

```go
// internal/worker/pool.go

type Pool struct {
    jobs   chan Job        // buffered channel = the work queue
    wg     sync.WaitGroup // tracks running goroutines
    logger *slog.Logger
}

type Job func(ctx context.Context) error
```

### The Critical Detail: `wg.Add(1)` Before `go`

```go
func New(ctx context.Context, workers int, queueSize int, logger *slog.Logger) *Pool {
    p := &Pool{
        jobs:   make(chan Job, queueSize),
        logger: logger,
    }
    for i := 0; i < workers; i++ {
        p.wg.Add(1)    // ← BEFORE launching the goroutine
        go p.run(ctx)
    }
    return p
}
```

Why must `wg.Add(1)` precede `go p.run(ctx)` and not be inside `run`?

```
If it were inside run():

launcher goroutine: go p.run(ctx)   (goroutine scheduled but not yet running)
launcher goroutine: pool.Stop()     (calls wg.Wait() — but wg counter is 0!)
launcher goroutine: Wait() returns  (thinks it is done)
run goroutine:      wg.Add(1)       (too late — Wait already returned)
run goroutine:      executes job    (pool "stopped" but work is still happening)
```

`wg.Add` before `go` guarantees the counter is incremented **before** any possible call to `Wait`.

### The Worker Loop

```go
func (p *Pool) run(jobCtx context.Context) {
    defer p.wg.Done()
    for job := range p.jobs {
        if err := job(jobCtx); err != nil {
            p.logger.ErrorContext(jobCtx, "worker job failed", slog.Any("error", err))
            // error logged, worker continues — not propagated
        }
        p.executed.Add(1)
    }
}
```

There is exactly one exit condition: `Stop()` closes the channel, `range` drains what
is left, and the worker returns. There is deliberately **no** `case <-ctx.Done()` — a
worker that exits on cancellation abandons whatever is still queued, which is precisely
the guarantee `Stop` makes. The cost is that `Stop` is mandatory: a pool that is never
stopped leaks its goroutines.

**`jobCtx` is not the lifecycle context.** It is handed to every job and must outlive
the shutdown signal, otherwise every drained job fails at its first network or database
call. `main.go` derives it once:

```go
drainCtx, cancelDrain := context.WithTimeout(
    context.WithoutCancel(ctx), cfg.WorkerDrainTimeout,
)
defer cancelDrain()
workerPool := worker.New(drainCtx, cfg.WorkerCount, cfg.WorkerQueue, logger)
```

**A job that returns an error does not kill the worker.** The pool logs and continues — critical for the outbox: one failing message must not stop delivery of others.

### `Submit` — Non-Blocking with Backpressure

```go
func (p *Pool) Submit(job Job) error {
    select {
    case p.jobs <- job:
        return nil
    default:
        return ErrPoolFull  // never blocks
    }
}
```

`Submit` never blocks. If the queue is full, it returns `ErrPoolFull` immediately. The caller decides what to do — for the outbox poller, the behaviour is to abandon the current batch and retry on the next tick.

### `Stop` — Graceful Drain

```go
func (p *Pool) Stop() {
    close(p.jobs) // signal workers to stop
    p.wg.Wait()   // wait for all in-flight jobs to finish
}
```

`close(p.jobs)` does not discard queued jobs — workers continue consuming until the queue is drained, then exit when they receive `!ok`. `wg.Wait` blocks until the last worker finishes. **No in-flight job is ever dropped.**

---

## Part 2: The Transactional Outbox

### The Table

```sql
-- migrations/004_create_outbox.sql

CREATE TABLE outbox_messages (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id UUID        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,          -- NULL = not yet delivered
    attempts     INT         NOT NULL DEFAULT 0,
    last_error   TEXT
);

-- Partial index: only indexes undelivered rows (processed_at IS NULL).
-- This is exactly the set the poller queries on every tick.
CREATE INDEX idx_outbox_unprocessed
    ON outbox_messages (created_at)
    WHERE processed_at IS NULL;
```

The partial index is essential. Without it the poller would do a full table scan. With it, the index only grows with undelivered messages — which under normal conditions is a small set.

### The Atomic Write in Service

```go
// internal/customer/service.go

func (s *Service) Register(ctx context.Context, cmd RegisterCmd) (uuid.UUID, error) {
    // ... validations ...

    tx, err := s.db.Begin(ctx)
    if err != nil {
        return uuid.Nil, fmt.Errorf("customer.Service.Register: begin tx: %w", err)
    }
    defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

    // Both writes in the same transaction — atomicity guaranteed by Postgres
    if err := s.repo.SaveTx(ctx, tx, customer); err != nil {
        return uuid.Nil, fmt.Errorf("customer.Service.Register: save: %w", err)
    }
    if err := s.appendOutbox(ctx, tx, "CustomerRegistered", customer.ID, payload); err != nil {
        return uuid.Nil, fmt.Errorf("customer.Service.Register: outbox: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return uuid.Nil, fmt.Errorf("customer.Service.Register: commit: %w", err)
    }

    return customer.ID, nil
}
```

**The `defer tx.Rollback(ctx)`**: after a successful `Commit`, `Rollback` returns an error that is discarded with `_`. After any error before `Commit`, the defer performs the rollback. This is the idiomatic pgx pattern — `Rollback` is a no-op after `Commit`, so there is no race condition.

**Why does `Service` accept `Beginner` instead of `*pgxpool.Pool`?**

```go
type Beginner interface {
    Begin(ctx context.Context) (pgx.Tx, error)
}
```

Interfaces at the consumer. `Service` only needs to open transactions — it does not need to know the backend is a Postgres pool. Unit tests inject a `stubBeginner` that returns a `stubTx` without opening a real connection.

### The `OutboxStore` Interface

```go
// internal/outbox/store.go

type OutboxStore interface {
    SaveTx(ctx context.Context, tx pgx.Tx, msg OutboxMessage) error
    ClaimBatch(ctx context.Context, limit int, reclaimAfter time.Duration) ([]OutboxMessage, error)
    MarkProcessed(ctx context.Context, id uuid.UUID) error
    MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
}
```

`SaveTx` is the most important method. It **requires** a live `pgx.Tx` — it never opens its own transaction. This design guarantees that anyone calling `SaveTx` understands they are participating in an existing transaction. There is no way to call `SaveTx` "outside" a transaction without passing a nil `pgx.Tx`, which would panic on the first query.

### `ClaimBatch` — Contention-Free Locking in One Statement

```go
// internal/outbox/postgres.go

func (s *PostgresOutboxStore) ClaimBatch(ctx context.Context, limit int, reclaimAfter time.Duration) ([]OutboxMessage, error) {
    rows, err := s.pool.Query(ctx, `
        UPDATE outbox_messages
        SET locked_at = now()
        WHERE id IN (
            SELECT id FROM outbox_messages
            WHERE processed_at IS NULL
              AND (locked_at IS NULL OR locked_at < now() - $2::interval)
            ORDER BY created_at
            LIMIT $1
            FOR UPDATE SKIP LOCKED   -- ← critical for multi-replica deployments
        )
        RETURNING id, aggregate_id, event_type, payload, created_at, attempts, last_error
    `, limit, reclaimAfter)
    // ...
}
```

`FOR UPDATE` locks the selected rows. `SKIP LOCKED` skips rows already locked by another transaction.

**Why the `UPDATE` wrapper matters.** A bare `SELECT ... FOR UPDATE SKIP LOCKED` issued
through `pool.Query` runs in an implicit single-statement transaction: the locks are
released the moment the rows are returned, so a second poller polling a millisecond later
sees the same rows unlocked and claims them too. Folding the selection and the `locked_at`
write into one statement is what makes the exclusion hold — the `UPDATE`'s own transaction
holds the locks through the write.

```
Replica A: ClaimBatch → gets messages [1, 2, 3], locked_at = now()
Replica B: ClaimBatch → gets messages [4, 5, 6]
           (messages 1–3 are locked, so they are skipped)
```

**Reclaiming abandoned locks.** A poller that dies between claiming and marking would
strand its batch forever. `locked_at < now() - reclaimAfter` (5 minutes, `_reclaimAfter`
in `poller.go`) makes such a row claimable again. The window must exceed the worst-case
publish latency so a slow Kafka write is not reclaimed under the poller that owns it.

`MarkFailed` clears `locked_at` as well as recording the error, so a failed message is
retried on the very next tick instead of waiting out the reclaim window.

### The `Publisher` Interface

```go
// internal/outbox/outbox.go

type Publisher interface {
    Publish(ctx context.Context, msg OutboxMessage) error
}
```

Today the system uses `LogPublisher` — it simply logs the message. It is the development publisher:

```go
// internal/outbox/log_publisher.go

func (p *LogPublisher) Publish(ctx context.Context, msg OutboxMessage) error {
    p.logger.InfoContext(ctx, "outbox message published",
        slog.String("event_type", msg.EventType),
        slog.String("aggregate_id", msg.AggregateID.String()),
        slog.String("payload", string(msg.Payload)),
    )
    return nil
}
```

To add Kafka: implement `KafkaPublisher` satisfying the `Publisher` interface and swap the injection in `main.go`. The rest of the system does not change.

### The Poller

```go
// internal/outbox/poller.go

type Poller struct {
    store     OutboxStore
    publisher Publisher
    pool      *worker.Pool
    interval  time.Duration
    batchSize int
    logger    *slog.Logger
}

func (p *Poller) Run(ctx context.Context) {
    ticker := time.NewTicker(p.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return  // graceful shutdown
        case <-ticker.C:
            p.poll(ctx)
        }
    }
}
```

The poller runs in a separate goroutine launched in `main.go`. It stops when `ctx` is cancelled — the same context shared by the HTTP server and the worker pool.

### Job Dispatch

```go
func (p *Poller) poll(ctx context.Context) {
    msgs, err := p.store.ClaimBatch(ctx, p.batchSize, _reclaimAfter)
    if err != nil {
        p.logger.ErrorContext(ctx, "outbox poll failed", slog.Any("error", err))
        return
    }

    for _, msg := range msgs {
        msg := msg  // capture for the closure — required pre-Go 1.22
        err := p.pool.Submit(func(ctx context.Context) error {
            return p.deliver(ctx, msg)
        })
        if errors.Is(err, worker.ErrPoolFull) {
            p.logger.WarnContext(ctx, "worker pool full, skipping remainder of outbox batch")
            return  // next tick will retry unsubmitted messages
        }
    }
}

func (p *Poller) deliver(ctx context.Context, msg OutboxMessage) error {
    if err := p.publisher.Publish(ctx, msg); err != nil {
        _ = p.store.MarkFailed(ctx, msg.ID, err.Error())
        return fmt.Errorf("outbox.deliver %s: %w", msg.ID, err)
    }
    return p.store.MarkProcessed(ctx, msg.ID)
}
```

**What happens when the pool is full**: the poller abandons the remaining batch and returns. Those messages stay claimed until the reclaim window elapses, then become claimable again. No message is lost — just delayed.

---

## Graceful Shutdown Sequence

This is the most important sequence to understand. An incorrect shutdown can drop in-flight jobs.

```
1. OS signal (SIGTERM/SIGINT)
   └─→ signal.NotifyContext cancels root ctx

2. Poller.Run() receives <-ctx.Done()
   └─→ stops fetching from outbox

3. server.Start() receives <-ctx.Done()
   └─→ Echo.Shutdown() drains active requests (10s timeout)
   └─→ server.Start() returns

4. workerPool.Stop() is called in main.go
   └─→ close(p.jobs) signals workers to stop
   └─→ wg.Wait() blocks until all in-flight jobs finish
       (jobs already submitted to the pool complete normally)

5. defer pool.Close() executes
   └─→ Postgres connections released

6. Process exits 0
```

```go
// cmd/api/main.go

// Step 1: root ctx cancelled on OS signal
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// Step 2: poller stops when ctx is cancelled (separate goroutine)
go poller.Run(ctx)

// Step 3: HTTP server drains and returns
if err := server.Start(ctx, cfg, ...); err != nil { ... }

// Step 4: pool drains in-flight jobs
workerPool.Stop()

// Step 5: Postgres closes via defer pool.Close()
```

**Why doesn't the pool stop on `ctx` at all?**

Because stopping on cancellation and draining the queue are mutually exclusive. If a
worker returns on `ctx.Done()`, whatever is still buffered in the channel is discarded —
and at shutdown the buffer is exactly where the work that has not run yet lives. `Stop()`
closing the channel is the only exit path, so every queued job runs. The bound on how
long that may take is `WORKER_DRAIN_TIMEOUT` on the job context, not a cancellation of
the loop.

---

## Failure Map

| Failure | When | Consequence | Recovery |
|---------|------|-------------|----------|
| Process dies before `COMMIT` | During `Register/Update/Remove` | No rows in DB or outbox (implicit rollback) | None — the operation did not happen |
| Process dies after `COMMIT`, before poller delivers | After successful write | Message stays in `outbox_messages` with `processed_at NULL` | Next process start + poller delivers |
| Publisher fails delivery | During `poller.deliver` | `attempts++`, `last_error` updated | Next tick retries |
| Worker pool is full | During `poller.poll` | Current batch abandoned | Next tick fetches again |
| Poller dies after claiming, before marking | Between `ClaimBatch` and `MarkProcessed` | Rows stay `locked_at` set, `processed_at NULL` | Another poller reclaims after the 5m window |
| Postgres unreachable | During `ClaimBatch` | Error logged, tick skipped | Next tick retries |

**At-least-once delivery**: a message may be delivered more than once if the process dies between `Publish` and `MarkProcessed`. Consumers must be idempotent (processing the same message twice has no duplicate side effects).

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_COUNT` | `4` | Number of goroutines in the pool |
| `WORKER_QUEUE` | `100` | Buffered channel size (backpressure limit) |
| `OUTBOX_INTERVAL` | `5` | Seconds between outbox polls |
| `OUTBOX_BATCH` | `50` | Maximum messages per poll |

**Sizing `WORKER_COUNT`**: must be compatible with the number of connections available in `pgxpool` and the throughput of the publisher. If the publisher is Kafka at 1000 req/s and each job takes ~10ms, 10 workers saturate the publisher. Start at 4 and tune based on metrics.

**Sizing `OUTBOX_INTERVAL`**: smaller = lower delivery latency, more DB queries. For integrations that tolerate second-level delays, 5s is a reasonable starting point. For near-real-time, consider 1s with a well-tuned index.

---

## Tests

### Worker Pool — `internal/worker/pool_test.go`

All run with `-race` (mandatory for concurrent code):

| Test | What it verifies |
|------|-----------------|
| `TestPool_SubmitWithinCapacity` | All jobs execute |
| `TestPool_SubmitOverCapacity` | `ErrPoolFull` without blocking |
| `TestPool_ContextCancelledStopsWorkers` | Workers stop, in-flight job completes |
| `TestPool_JobError_PoolContinues` | Job error does not kill the pool |
| `TestPool_StopAfterCancel_NoDealock` | `Stop()` after cancellation does not deadlock |

### Outbox Poller — `internal/outbox/poller_test.go`

Using hand-written stubs (no Docker, no mockery):

| Test | What it verifies |
|------|-----------------|
| `TestPoller_HappyPath` | Message fetched → published → marked `processed` |
| `TestPoller_PublishFails_MarkedFailed` | `attempts++`, `last_error` set, `processed` not marked |
| `TestPoller_PoolFull_BatchSkipped` | Batch abandoned without panic when pool is full |
| `TestPoller_EmptyOutbox_NoMarkCalls` | No `MarkProcessed`/`MarkFailed` calls on empty outbox |

The `TestPoller_PoolFull_BatchSkipped` test has an important setup detail: it guarantees the worker is **actually** executing a blocking job before enqueueing the second job. Without this, there is a race condition between the submit and the worker draining the queue:

```go
workerBusy := make(chan struct{})

require.NoError(t, pool.Submit(func(ctx context.Context) error {
    close(workerBusy) // signal: worker is now busy
    <-block           // block until the test is done
    return nil
}))
<-workerBusy // wait until the worker is actually executing job1

// Now safe: the queue has exactly 1 free slot
require.NoError(t, pool.Submit(func(ctx context.Context) error { <-block; return nil }))
// Pool state: worker busy + queue full. Any further Submit returns ErrPoolFull.
```

---

## What NOT to Do

| Do not | Why |
|--------|-----|
| Publish events outside a transaction | Race condition between DB write and event publish. Processes die. |
| Call `SaveTx` outside a live transaction | The `pgx.Tx` parameter must be a real tx or the Exec will fail |
| Remove `FOR UPDATE SKIP LOCKED` | Multiple replicas will process the same messages twice |
| Let the publisher block indefinitely | Pool workers get stuck, queue fills, backpressure becomes deadlock |
| Assume exactly-once delivery | The outbox guarantees at-least-once. Consumers must be idempotent. |
| Set `processed_at` directly without `MarkProcessed` | Breaks the invariant that only the poller marks messages as delivered |
| Call `wg.Add` inside the goroutine | Position matters — see the WaitGroup section above |
