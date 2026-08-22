package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
)

const _testSecret = "test-secret-32-chars-long-enough!"

// signToken mints a token the middleware must accept, without going through the
// service — these tests are about the header, not about login.
func signToken(t *testing.T, sub string) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   sub,
		"email": "user@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})
	signed, err := token.SignedString([]byte(_testSecret))
	require.NoError(t, err)
	return signed
}

// TestJWTMiddleware_AuthorizationHeader covers the scheme check. The wrong
// scheme carrying an otherwise valid token is the interesting case: the old
// extractor sliced seven bytes off any long-enough header, so "Token <jwt>"
// reached the parser as a mangled token and came back as "invalid or expired"
// rather than as a request with no credentials.
func TestJWTMiddleware_AuthorizationHeader(t *testing.T) {
	valid := signToken(t, "5c2a0f52-6b1e-4a67-9f6a-2f0f1a1a8b21")

	tests := []struct {
		name       string
		header     string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "valid bearer token",
			header:     "Bearer " + valid,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no header at all",
			header:     "",
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "missing authorization header",
		},
		{
			name:       "non-Bearer scheme carrying a valid token",
			header:     "Token " + valid,
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "missing authorization header",
		},
		{
			name:       "Basic credentials",
			header:     "Basic dXNlcjpwYXNzd29yZA==",
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "missing authorization header",
		},
		{
			// Case-sensitive by choice: no client in this repository sends a
			// lowercase scheme, and RFC 7235 case-insensitivity can be added
			// when one does.
			name:       "lowercase scheme",
			header:     "bearer " + valid,
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "missing authorization header",
		},
		{
			name:       "bearer scheme with a token that does not parse",
			header:     "Bearer not-a-jwt",
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "invalid or expired token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			handlerCalled := false
			h := auth.JWTMiddleware(_testSecret)(func(c echo.Context) error {
				handlerCalled = true
				return c.NoContent(http.StatusOK)
			})

			if err := h(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusOK {
				assert.True(t, handlerCalled, "the wrapped handler must run")
				return
			}

			assert.False(t, handlerCalled, "the wrapped handler must not run")

			var body map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantMsg, body["message"])
		})
	}
}
