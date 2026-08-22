package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

const _contextKeyUserID = "user_id"

// ClaimsKey is the context key under which JWT claims are stored by the gRPC auth interceptor.
type claimsContextKey struct{}

var ClaimsKey = claimsContextKey{}

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

// extractBearerToken returns the token from an Authorization header, or "" if
// the header is absent or does not use the Bearer scheme.
//
// CutPrefix, not a length check and a slice: the old form accepted any header
// at least as long as "Bearer " and cut seven bytes off it, so "Token abcdefgh"
// arrived at the JWT parser as "abcdefgh" — a non-Bearer scheme treated as a
// malformed token rather than as no credentials at all.
func extractBearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}
