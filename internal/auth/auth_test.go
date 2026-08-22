package auth_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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

// ---- service seams ----

// stubRepo is an in-memory auth.Repository. FindByEmail is the only method the
// timing test exercises, and it must be uniformly cheap for both outcomes:
// anything it does differently for a hit and a miss would show up as the very
// signal the test is measuring.
type stubRepo struct {
	users map[string]auth.User
}

func newStubRepo() *stubRepo { return &stubRepo{users: make(map[string]auth.User)} }

func (r *stubRepo) Save(_ context.Context, u auth.User) error {
	r.users[u.Email] = u
	return nil
}

func (r *stubRepo) FindByEmail(_ context.Context, email string) (auth.User, error) {
	u, ok := r.users[email]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

func (r *stubRepo) FindByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrUserNotFound
}

func newService(t *testing.T) *auth.Service {
	t.Helper()
	return auth.NewService(newStubRepo(), _testSecret, time.Hour, discardLogger())
}

// TestValidateToken_Sentinel keeps token failures and credential failures
// distinguishable — a rejected token says nothing about a password.
func TestValidateToken_Sentinel(t *testing.T) {
	svc := newService(t)

	tests := []struct {
		name  string
		token string
	}{
		{name: "not a jwt at all", token: "garbage"},
		{name: "empty", token: ""},
		{name: "signed with another key", token: foreignToken(t)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := svc.ValidateToken(tt.token)
			assert.Nil(t, claims)
			assert.ErrorIs(t, err, auth.ErrInvalidToken)
			assert.NotErrorIs(t, err, auth.ErrInvalidPassword)
		})
	}
}

// foreignToken returns a structurally valid token signed with the wrong key.
func foreignToken(t *testing.T) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "e0b3d5a4-2b3c-4d5e-8f90-1a2b3c4d5e6f",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("a-different-secret-of-equal-length"))
	require.NoError(t, err)
	return signed
}

// TestLogin_UnknownEmailIsIndistinguishable covers the response half of the
// enumeration story: an unknown address and a wrong password must be reported
// identically.
func TestLogin_UnknownEmailIsIndistinguishable(t *testing.T) {
	auth.SetBcryptCostForTest(t, bcrypt.MinCost)

	repo := newStubRepo()
	svc := auth.NewService(repo, _testSecret, time.Hour, discardLogger())
	ctx := context.Background()

	_, err := svc.Register(ctx, auth.RegisterCmd{
		Email: "known@example.com", Name: "Known", Password: "correct-horse",
	})
	require.NoError(t, err)

	_, wrongPassword := svc.Login(ctx, auth.LoginCmd{
		Email: "known@example.com", Password: "wrong-password",
	})
	_, unknownEmail := svc.Login(ctx, auth.LoginCmd{
		Email: "nobody@example.com", Password: "correct-horse",
	})

	assert.ErrorIs(t, wrongPassword, auth.ErrInvalidPassword)
	assert.ErrorIs(t, unknownEmail, auth.ErrInvalidPassword)
	assert.Equal(t, wrongPassword.Error(), unknownEmail.Error(),
		"the two failures must be reported identically")

	// The placeholder hash must never let anyone in.
	_, err = svc.Login(ctx, auth.LoginCmd{
		Email: "nobody@example.com", Password: "timing-equalisation-placeholder",
	})
	assert.ErrorIs(t, err, auth.ErrInvalidPassword)
}

// TestLogin_TimingParity covers the timing half. Before the fix the unknown
// path skipped bcrypt entirely, so it returned in roughly the time of a map
// lookup while the known path paid a full hash comparison — a difference an
// attacker can measure over the network.
//
// Medians, not means: this runs on a shared CI machine, and one scheduler
// hiccup would swing an average. The 50% threshold is deliberately loose — the
// defect it guards against is a difference of an order of magnitude, and a
// tighter bound would only buy flakes.
func TestLogin_TimingParity(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement is too noisy for -short")
	}

	// Cost 12 would make this test take minutes. The comparison is between two
	// code paths, and lowering the cost lowers both of them equally.
	auth.SetBcryptCostForTest(t, bcrypt.MinCost)

	repo := newStubRepo()
	svc := auth.NewService(repo, _testSecret, time.Hour, discardLogger())
	ctx := context.Background()

	_, err := svc.Register(ctx, auth.RegisterCmd{
		Email: "known@example.com", Name: "Known", Password: "correct-horse",
	})
	require.NoError(t, err)

	const samples = 50
	known := make([]time.Duration, 0, samples)
	unknown := make([]time.Duration, 0, samples)

	// Interleaved so a drifting machine load hits both series equally.
	for range samples {
		known = append(known, timeLogin(svc, ctx, "known@example.com"))
		unknown = append(unknown, timeLogin(svc, ctx, "nobody@example.com"))
	}

	knownMedian, unknownMedian := median(known), median(unknown)
	diff := knownMedian - unknownMedian
	if diff < 0 {
		diff = -diff
	}

	assert.Less(t, float64(diff), 0.5*float64(knownMedian),
		"unknown-email login (%s) must cost about as much as known-email login (%s)",
		unknownMedian, knownMedian)
}

func timeLogin(svc *auth.Service, ctx context.Context, email string) time.Duration {
	start := time.Now()
	_, _ = svc.Login(ctx, auth.LoginCmd{Email: email, Password: "wrong-password"})
	return time.Since(start)
}

func median(ds []time.Duration) time.Duration {
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}
