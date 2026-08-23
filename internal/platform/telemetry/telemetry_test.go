package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetrytest"
)

func TestOTelHandler_InjectsTraceAndSpanID(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var buf bytes.Buffer
	handler := telemetry.NewOTelHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler)

	ctx, span := otel.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	logger.InfoContext(ctx, "hello from traced context")

	out := buf.String()
	assert.True(t, strings.Contains(out, "trace_id"), "expected trace_id in log output")
	assert.True(t, strings.Contains(out, "span_id"), "expected span_id in log output")
}

func TestOTelHandler_NoopWhenNoSpan(t *testing.T) {
	var buf bytes.Buffer
	handler := telemetry.NewOTelHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler)

	logger.InfoContext(context.Background(), "no span here")

	out := buf.String()
	require.NotEmpty(t, out)
	assert.False(t, strings.Contains(out, "trace_id"), "unexpected trace_id without active span")
}

func TestOTelHandler_WithAttrs_PreservesOTelBridge(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var buf bytes.Buffer
	base := telemetry.NewOTelHandler(slog.NewJSONHandler(&buf, nil))
	child := slog.New(base.WithAttrs([]slog.Attr{slog.String("service", "test")}))

	ctx, span := otel.Tracer("test").Start(context.Background(), "span")
	defer span.End()

	child.InfoContext(ctx, "bridged log via WithAttrs child")

	out := buf.String()
	assert.True(t, strings.Contains(out, "trace_id"), "expected trace_id after WithAttrs")
	assert.True(t, strings.Contains(out, "service"), "expected custom attr after WithAttrs")
}

// TestInitMetrics_RecordsValues replaces a test that asserted ten instruments
// were non-nil. That test could not fail: init() pre-binds every instrument to
// a noop so callers never receive nil, so it passed throughout the period in
// which four of the ten were never incremented anywhere in the codebase. It was
// the mechanism by which the gap survived.
//
// This asserts the thing that matters instead — that a value recorded through
// each instrument arrives at a reader under the expected name.
//
// Not parallel: the global meter provider is process-wide state.
func TestInitMetrics_RecordsValues(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		instrument string
		record     func()
		want       int64
	}{
		{
			name:       "http requests",
			instrument: "http.requests.total",
			record:     func() { telemetry.HTTPRequestsTotal.Add(ctx, 1) },
			want:       1,
		},
		{
			name:       "customer registrations",
			instrument: "customer.registrations.total",
			record:     func() { telemetry.CustomerRegistrations.Add(ctx, 3) },
			want:       3,
		},
		{
			name:       "customer removals",
			instrument: "customer.removals.total",
			record:     func() { telemetry.CustomerRemovals.Add(ctx, 1) },
			want:       1,
		},
		{
			name:       "db query errors",
			instrument: "db.query.errors.total",
			record:     func() { telemetry.DBQueryErrors.Add(ctx, 2) },
			want:       2,
		},
		{
			name:       "cache hits",
			instrument: "cache.hits.total",
			record:     func() { telemetry.CacheHits.Add(ctx, 5) },
			want:       5,
		},
		{
			name:       "cache misses",
			instrument: "cache.misses.total",
			record:     func() { telemetry.CacheMisses.Add(ctx, 4) },
			want:       4,
		},
		{
			name:       "outbox messages published",
			instrument: "outbox.messages.published.total",
			record:     func() { telemetry.OutboxMessagesPublished.Add(ctx, 7) },
			want:       7,
		},
		{
			name:       "outbox publish failures",
			instrument: "outbox.publish.failures.total",
			record:     func() { telemetry.OutboxPublishFailures.Add(ctx, 1) },
			want:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counters := telemetrytest.CollectCounters(t, tt.record)

			assert.Equal(t, tt.want, counters.Counter(tt.instrument),
				"%s must record the value passed to Add", tt.instrument)
		})
	}
}

// TestInitMetrics_RecordsHistograms covers the two Float64 instruments, which
// land as histograms rather than sums and so are not visible to CollectCounters.
func TestInitMetrics_RecordsHistograms(t *testing.T) {
	ctx := context.Background()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		require.NoError(t, telemetry.InitMetrics())
		require.NoError(t, provider.Shutdown(ctx))
	})

	require.NoError(t, telemetry.InitMetrics())

	telemetry.HTTPRequestDuration.Record(ctx, 0.25)
	telemetry.DBQueryDuration.Record(ctx, 0.5)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	sums := map[string]float64{}
	counts := map[string]uint64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, dp := range hist.DataPoints {
				sums[m.Name] += dp.Sum
				counts[m.Name] += dp.Count
			}
		}
	}

	assert.EqualValues(t, 1, counts["http.request.duration"])
	assert.InDelta(t, 0.25, sums["http.request.duration"], 1e-9)
	assert.EqualValues(t, 1, counts["db.query.duration"])
	assert.InDelta(t, 0.5, sums["db.query.duration"], 1e-9)
}

// TestInitMetrics_IsIdempotent guards the pattern main() relies on: init()
// binds noop instruments so nothing is ever nil, and main calls InitMetrics
// again after Setup to rebind them to the real provider.
func TestInitMetrics_IsIdempotent(t *testing.T) {
	require.NoError(t, telemetry.InitMetrics())
	require.NoError(t, telemetry.InitMetrics())

	counters := telemetrytest.CollectCounters(t, func() {
		telemetry.CacheHits.Add(context.Background(), 1)
	})
	assert.EqualValues(t, 1, counters.Counter("cache.hits.total"),
		"a rebound instrument must record against the current provider")
}
