package telemetry

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Package-level metric instruments. All are initialized by InitMetrics().
// When OTel is disabled the global meter is a noop — all operations are zero-cost.
var (
	// HTTP
	HTTPRequestDuration metric.Float64Histogram
	HTTPRequestsTotal   metric.Int64Counter

	// Customer domain
	CustomerRegistrations metric.Int64Counter
	CustomerRemovals      metric.Int64Counter

	// Database
	DBQueryDuration metric.Float64Histogram
	DBQueryErrors   metric.Int64Counter

	// Cache
	CacheHits   metric.Int64Counter
	CacheMisses metric.Int64Counter

	// Outbox
	OutboxMessagesPublished metric.Int64Counter
	OutboxPublishFailures   metric.Int64Counter
)

// InitMetrics registers all metric instruments with the global meter.
// Call once after Setup() (or without it — noop meter is used when OTel is disabled).
func InitMetrics() error {
	m := otel.Meter("go-blueprint")
	var err error

	if HTTPRequestDuration, err = m.Float64Histogram("http.request.duration",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithUnit("s"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: http.request.duration: %w", err)
	}

	if HTTPRequestsTotal, err = m.Int64Counter("http.requests.total",
		metric.WithDescription("Total number of HTTP requests"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: http.requests.total: %w", err)
	}

	if CustomerRegistrations, err = m.Int64Counter("customer.registrations.total",
		metric.WithDescription("Total number of customer registrations"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: customer.registrations.total: %w", err)
	}

	if CustomerRemovals, err = m.Int64Counter("customer.removals.total",
		metric.WithDescription("Total number of customer removals"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: customer.removals.total: %w", err)
	}

	if DBQueryDuration, err = m.Float64Histogram("db.query.duration",
		metric.WithDescription("Database query duration in seconds"),
		metric.WithUnit("s"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: db.query.duration: %w", err)
	}

	if DBQueryErrors, err = m.Int64Counter("db.query.errors.total",
		metric.WithDescription("Total number of database query errors"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: db.query.errors.total: %w", err)
	}

	if CacheHits, err = m.Int64Counter("cache.hits.total",
		metric.WithDescription("Total number of cache hits"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: cache.hits.total: %w", err)
	}

	if CacheMisses, err = m.Int64Counter("cache.misses.total",
		metric.WithDescription("Total number of cache misses"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: cache.misses.total: %w", err)
	}

	if OutboxMessagesPublished, err = m.Int64Counter("outbox.messages.published.total",
		metric.WithDescription("Total number of outbox messages published"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: outbox.messages.published.total: %w", err)
	}

	if OutboxPublishFailures, err = m.Int64Counter("outbox.publish.failures.total",
		metric.WithDescription("Total number of outbox publish failures"),
	); err != nil {
		return fmt.Errorf("telemetry.InitMetrics: outbox.publish.failures.total: %w", err)
	}

	return nil
}
