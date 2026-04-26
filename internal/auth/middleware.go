package auth

import (
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

const _contextKeyUserID = "user_id"

func JWTMiddleware(secret string) echo.MiddlewareFunc {
	key := []byte(secret)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tokenStr := extractBearerToken(c.Request())
			if tokenStr == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing authorization header")
			}

			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return key, nil
			})
			if err != nil || !token.Valid {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired token")
			}

			sub, err := claims.GetSubject()
			if err != nil || sub == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid token claims")
			}

			c.Set(_contextKeyUserID, sub)
			return next(c)
		}
	}
}

func extractBearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) < len(prefix) {
		return ""
	}
	return header[len(prefix):]
}
