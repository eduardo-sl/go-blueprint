// Package telemetrytest provides a manual-reader harness for asserting on the
// values the telemetry package's metric instruments actually record.
//
// It exists because the instruments are package-level globals bound by
// telemetry.InitMetrics. Asserting that one is non-nil cannot fail — init()
// pre-binds every one of them to a noop — so a test written that way passes
// whether or not the instrument is ever incremented. That is precisely how four
// instruments stayed dormant behind a passing test. This package makes the
// recorded value the thing under assertion instead.
//
// It follows the shape of the SDK's own tracetest helper, and is test-only: no
// non-test code imports it.
package telemetrytest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
)

// CollectCounters installs a manual-reader meter provider, rebinds every
// telemetry instrument to it, runs fn, and returns the recorded Int64 counter
// sums keyed by instrument name. Instruments that recorded nothing are absent
// from the map, so an assertion of zero should use Counter.
//
// The global meter provider is process-wide state, so a test calling this must
// NOT call t.Parallel(). Cleanup restores the previous provider and rebinds the
// instruments to it, leaving later tests unaffected.
func CollectCounters(t *testing.T, fn func()) Counters {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	if err := telemetry.InitMetrics(); err != nil {
		t.Fatalf("telemetrytest: bind instruments: %v", err)
	}

	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		// Rebind to the restored provider so instruments do not keep pointing
		// at a meter whose reader is about to be collected no further.
		if err := telemetry.InitMetrics(); err != nil {
			t.Errorf("telemetrytest: rebind instruments: %v", err)
		}
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("telemetrytest: shutdown provider: %v", err)
		}
	})

	fn()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("telemetrytest: collect: %v", err)
	}

	counters := Counters{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				counters[m.Name] += dp.Value
			}
		}
	}
	return counters
}

// Counters holds recorded Int64 counter sums by instrument name.
type Counters map[string]int64

// Counter returns the recorded sum for name, or zero when the instrument
// recorded nothing. Distinguishing "absent" from "zero" is not useful here: a
// counter that was never incremented and one incremented by zero mean the same
// thing to a dashboard.
func (c Counters) Counter(name string) int64 { return c[name] }
