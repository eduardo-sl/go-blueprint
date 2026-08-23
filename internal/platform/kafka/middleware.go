package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Middleware wraps a Handler to add cross-cutting behavior.
type Middleware func(Handler) Handler

// Chain applies middlewares in order: first middleware is outermost.
func Chain(h Handler, mw ...Middleware) Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// WithLogging logs each record before dispatch and on error.
func WithLogging(logger *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
			logger.InfoContext(ctx, "kafka dispatching record",
				"topic", record.Topic,
				"partition", record.Partition,
				"offset", record.Offset,
				"event_type", headerValue(record, "event_type"),
			)
			err := next.Handle(ctx, record)
			if err != nil {
				logger.ErrorContext(ctx, "kafka handler returned error",
					"topic", record.Topic,
					"offset", record.Offset,
					"error", err,
				)
			}
			return err
		})
	}
}

// WithRecovery catches panics from downstream handlers and converts them to errors.
func WithRecovery(logger *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, record *kgo.Record) (err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.ErrorContext(ctx, "kafka handler panic recovered",
						"topic", record.Topic,
						"partition", record.Partition,
						"offset", record.Offset,
						"panic", r,
					)
					err = fmt.Errorf("kafka: handler panicked: %v", r)
				}
			}()
			return next.Handle(ctx, record)
		})
	}
}

// WithIdempotency skips records whose message_id header was already seen.
// The store is in-memory and bounded: it resets on restart, and evicts
// oldest-first past limit so a long-lived consumer cannot grow without bound.
// For durability across restarts, back this with Redis instead.
func WithIdempotency(logger *slog.Logger, limit int) Middleware {
	seen := newDedupSet(limit)
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
			msgID := headerValue(record, "message_id")
			if msgID == "" {
				return next.Handle(ctx, record)
			}
			if seen.Add(msgID) {
				logger.DebugContext(ctx, "kafka idempotency: duplicate skipped",
					"message_id", msgID,
					"topic", record.Topic,
					"offset", record.Offset,
				)
				return nil
			}
			return next.Handle(ctx, record)
		})
	}
}

// dedupSet is a bounded set of message IDs with FIFO eviction. A plain map
// guarded by a mutex, not a concurrent map: eviction needs insertion order, and
// dispatch is single-goroutine per consumer, so there is no contention to win.
type dedupSet struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string // insertion order, for FIFO eviction
	limit int
}

// _defaultDedupLimit is used when a caller passes a non-positive limit, so a
// misconfigured bound degrades to a sane one rather than to an unbounded map.
const _defaultDedupLimit = 10_000

func newDedupSet(limit int) *dedupSet {
	if limit <= 0 {
		limit = _defaultDedupLimit
	}
	return &dedupSet{
		seen:  make(map[string]struct{}, limit),
		order: make([]string, 0, limit),
		limit: limit,
	}
}

// Add records id and reports whether it was already present. Once limit entries
// are held, the oldest is evicted — a message redelivered after limit newer
// messages have passed is therefore treated as new, which is the trade the
// in-memory bound buys.
func (d *dedupSet) Add(id string) (duplicate bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[id]; ok {
		return true
	}
	if len(d.order) >= d.limit {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
	d.seen[id] = struct{}{}
	d.order = append(d.order, id)
	return false
}
