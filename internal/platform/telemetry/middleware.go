package telemetry

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// EchoMiddleware returns an Echo middleware that starts a trace span per request.
// It delegates W3C TraceContext header propagation to otelecho and enriches the
// span with the route template (not the raw path) to avoid high-cardinality data.
func EchoMiddleware(serviceName string) echo.MiddlewareFunc {
	base := otelecho.Middleware(serviceName)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return base(func(c echo.Context) error {
			span := trace.SpanFromContext(c.Request().Context())

			span.SetAttributes(
				attribute.String("http.route", c.Path()),
				attribute.String("http.request_id", c.Response().Header().Get(echo.HeaderXRequestID)),
			)

			err := next(c)

			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else if status := c.Response().Status; status >= 400 {
				span.SetStatus(codes.Error, http.StatusText(status))
			}

			return err
		})
	}
}
