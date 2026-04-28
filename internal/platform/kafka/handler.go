package kafka

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Handler processes a single Kafka record.
// Implementations MUST be idempotent — the same record may be delivered more than once.
type Handler interface {
	Handle(ctx context.Context, record *kgo.Record) error
}

// HandlerFunc is an adapter to allow use of ordinary functions as Handlers.
type HandlerFunc func(ctx context.Context, record *kgo.Record) error

func (f HandlerFunc) Handle(ctx context.Context, record *kgo.Record) error {
	return f(ctx, record)
}

// headerValue returns the value of the first Kafka record header matching key.
// Returns an empty string when the header is absent.
func headerValue(record *kgo.Record, key string) string {
	for _, h := range record.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
