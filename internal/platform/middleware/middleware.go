package middleware

import (
	"log/slog"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

func Register(e *echo.Echo, logger *slog.Logger) {
	e.Use(echomiddleware.RequestID())
	e.Use(echomiddleware.Recover())
	e.Use(echomiddleware.CORS())
	e.Use(slogMiddleware(logger))
}

func slogMiddleware(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)

			req := c.Request()
			res := c.Response()

			logger.InfoContext(req.Context(), "request",
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Int("status", res.Status),
				slog.String("request_id", c.Response().Header().Get(echo.HeaderXRequestID)),
			)

			return err
		}
	}
}
