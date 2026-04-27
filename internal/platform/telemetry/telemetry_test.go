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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
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

func TestInitMetrics_RegistersAllInstruments(t *testing.T) {
	require.NoError(t, telemetry.InitMetrics())

	assert.NotNil(t, telemetry.HTTPRequestDuration)
	assert.NotNil(t, telemetry.HTTPRequestsTotal)
	assert.NotNil(t, telemetry.CustomerRegistrations)
	assert.NotNil(t, telemetry.CustomerRemovals)
	assert.NotNil(t, telemetry.DBQueryDuration)
	assert.NotNil(t, telemetry.DBQueryErrors)
	assert.NotNil(t, telemetry.CacheHits)
	assert.NotNil(t, telemetry.CacheMisses)
	assert.NotNil(t, telemetry.OutboxMessagesPublished)
	assert.NotNil(t, telemetry.OutboxPublishFailures)
}
