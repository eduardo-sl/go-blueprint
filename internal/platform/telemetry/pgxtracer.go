package telemetry

import (
	"context"
	"time"

	pgx "github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type pgxSpanKey struct{}

type spanWithStart struct {
	span  trace.Span
	start time.Time
}

// PgxTracer implements pgx.QueryTracer to create OTel spans for every SQL query.
// Attach to pgxpool.Config.ConnConfig.Tracer to instrument all database queries.
// When no OTel provider is configured it degrades to a noop.
type PgxTracer struct {
	tracer trace.Tracer
}

func NewPgxTracer() *PgxTracer {
	return &PgxTracer{tracer: otel.Tracer("pgx")}
}

func (t *PgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "pgx.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.statement", data.SQL),
		),
	)
	return context.WithValue(ctx, pgxSpanKey{}, spanWithStart{span: span, start: time.Now()})
}

func (t *PgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	val, ok := ctx.Value(pgxSpanKey{}).(spanWithStart)
	if !ok {
		return
	}

	if DBQueryDuration != nil {
		DBQueryDuration.Record(ctx, time.Since(val.start).Seconds())
	}

	if data.Err != nil {
		val.span.RecordError(data.Err)
		val.span.SetStatus(codes.Error, data.Err.Error())
		if DBQueryErrors != nil {
			DBQueryErrors.Add(ctx, 1)
		}
	}

	val.span.End()
}
