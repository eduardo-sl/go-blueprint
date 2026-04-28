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

// WithIdempotency skips records whose message_id header was already processed.
// The dedup store is in-memory (sync.Map) — it resets on restart.
// For durability across restarts, use a Redis or database-backed store instead.
func WithIdempotency(logger *slog.Logger) Middleware {
	var seen sync.Map
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
			msgID := headerValue(record, "message_id")
			if msgID == "" {
				return next.Handle(ctx, record)
			}
			if _, loaded := seen.LoadOrStore(msgID, true); loaded {
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
