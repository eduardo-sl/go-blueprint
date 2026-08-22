package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/worker"
)

// _reclaimAfter bounds how long a claimed message may stay locked before
// another poller may take it over. It must exceed the worst-case publish
// latency so a slow Kafka write is not reclaimed under the poller that owns it,
// and stay short enough that a crashed poller's backlog recovers within one
// deploy cycle.
const _reclaimAfter = 5 * time.Minute

// _maxBackoff caps the exponential retry delay. Past this point the extra wait
// buys nothing: a dependency that has been down for five minutes is an incident,
// not a blip, and the attempt ceiling will retire the message soon enough.
const _maxBackoff = 5 * time.Minute

// Poller polls the outbox table at a fixed interval and publishes
// unprocessed messages via the configured Publisher.
// It submits each delivery as a job to the worker pool for concurrent processing.
// A message that fails maxAttempts times is retired rather than retried forever.
type Poller struct {
	store       OutboxStore
	publisher   Publisher
	pool        *worker.Pool
	interval    time.Duration
	batchSize   int
	maxAttempts int
	logger      *slog.Logger
}

// NewPoller creates a Poller. Call Run in a goroutine to start polling.
func NewPoller(
	store OutboxStore,
	publisher Publisher,
	pool *worker.Pool,
	interval time.Duration,
	batchSize int,
	maxAttempts int,
	logger *slog.Logger,
) *Poller {
	return &Poller{
		store:       store,
		publisher:   publisher,
		pool:        pool,
		interval:    interval,
		batchSize:   batchSize,
		maxAttempts: maxAttempts,
		logger:      logger,
	}
}

// Run starts the polling loop. It exits when ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	// ClaimBatch locks the rows it returns, so a second poller instance ticking
	// at the same moment gets a disjoint batch.
	msgs, err := p.store.ClaimBatch(ctx, p.batchSize, _reclaimAfter, p.maxAttempts)
	if err != nil {
		p.logger.ErrorContext(ctx, "outbox poll failed", slog.Any("error", err))
		return
	}

	for _, msg := range msgs {
		msg := msg // capture for goroutine — required pre-Go 1.22
		err := p.pool.Submit(func(ctx context.Context) error {
			return p.deliver(ctx, msg)
		})
		if errors.Is(err, worker.ErrPoolFull) {
			p.logger.WarnContext(ctx, "worker pool full, skipping remainder of outbox batch")
			return // next tick will retry unsubmitted messages
		}
	}
}

func (p *Poller) deliver(ctx context.Context, msg OutboxMessage) error {
	err := p.publisher.Publish(ctx, msg)
	if err == nil {
		return p.store.MarkProcessed(ctx, msg.ID)
	}

	attempts := msg.Attempts + 1
	if attempts >= p.maxAttempts {
		// The only place a message stops being retried, so it is loud: a
		// non-zero rate here means events are being dropped from the pipeline.
		p.logger.ErrorContext(ctx, "outbox message exhausted retries",
			slog.String("message_id", msg.ID.String()),
			slog.Int("attempts", attempts),
			slog.Any("error", err),
		)
		markErr := p.store.MarkExhausted(ctx, msg.ID, err.Error())
		return fmt.Errorf("outbox.deliver %s: %w", msg.ID, errors.Join(err, markErr))
	}

	markErr := p.store.MarkFailed(ctx, msg.ID, err.Error(), p.backoff(attempts))
	return fmt.Errorf("outbox.deliver %s: %w", msg.ID, errors.Join(err, markErr))
}

// backoff returns interval * 2^attempts, capped at _maxBackoff. It doubles in a
// loop rather than shifting so the cap is reached before the multiplication can
// overflow time.Duration, and it lives here rather than in SQL so the schedule
// is unit-testable without a database.
func (p *Poller) backoff(attempts int) time.Duration {
	d := p.interval
	for range attempts {
		if d >= _maxBackoff {
			return _maxBackoff
		}
		d *= 2
	}
	return min(d, _maxBackoff)
}
