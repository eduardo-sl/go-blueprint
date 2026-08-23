package customer_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func eventRecord(messageID, eventType string) *kgo.Record {
	r := &kgo.Record{Topic: "customers.events", Value: []byte(`{}`)}
	if messageID != "" {
		r.Headers = append(r.Headers, kgo.RecordHeader{Key: "message_id", Value: []byte(messageID)})
	}
	if eventType != "" {
		r.Headers = append(r.Headers, kgo.RecordHeader{Key: "event_type", Value: []byte(eventType)})
	}
	return r
}

// TestEventHandler_DoesNotDedup pins the division of labour: dedup lives in
// kafka.WithIdempotency, which composes and is bounded. The handler used to
// carry its own unbounded copy of the same idea, so the same record arriving
// twice was silently dropped even when nothing had asked for that.
//
// Handed the same record twice with no chain around it, the handler must
// process both.
func TestEventHandler_DoesNotDedup(t *testing.T) {
	var logs bytes.Buffer
	h := customer.NewEventHandler(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	record := eventRecord("msg-1", "CustomerRegistered")

	require.NoError(t, h.Handle(context.Background(), record))
	require.NoError(t, h.Handle(context.Background(), record))

	assert.Equal(t, 2, strings.Count(logs.String(), "customer registered event processed"),
		"the handler must not skip a repeat; that decision belongs to the middleware")
	assert.NotContains(t, logs.String(), "duplicate")
}

// TestEventHandler_Dispatch covers the routing the handler is now solely
// responsible for, including the two records it must accept without failing:
// an unknown event type and a record with no message_id. Both return nil so a
// single odd record cannot wedge the partition behind an uncommitted offset.
func TestEventHandler_Dispatch(t *testing.T) {
	tests := []struct {
		name      string
		record    *kgo.Record
		wantLog   string
		unwanted  string
		wantNoErr bool
	}{
		{
			name:      "registered",
			record:    eventRecord("m1", "CustomerRegistered"),
			wantLog:   "customer registered event processed",
			wantNoErr: true,
		},
		{
			name:      "updated",
			record:    eventRecord("m2", "CustomerUpdated"),
			wantLog:   "customer updated event processed",
			wantNoErr: true,
		},
		{
			name:      "removed",
			record:    eventRecord("m3", "CustomerRemoved"),
			wantLog:   "customer removed event processed",
			wantNoErr: true,
		},
		{
			name:      "unknown type is skipped, not failed",
			record:    eventRecord("m4", "SomethingElse"),
			wantLog:   "kafka unknown event type, skipping",
			wantNoErr: true,
		},
		{
			name:      "missing message_id is skipped, not failed",
			record:    eventRecord("", "CustomerRegistered"),
			wantLog:   "kafka record missing message_id header",
			unwanted:  "customer registered event processed",
			wantNoErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			h := customer.NewEventHandler(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

			err := h.Handle(context.Background(), tt.record)

			if tt.wantNoErr {
				require.NoError(t, err)
			}
			assert.Contains(t, logs.String(), tt.wantLog)
			if tt.unwanted != "" {
				assert.NotContains(t, logs.String(), tt.unwanted)
			}
		})
	}
}
