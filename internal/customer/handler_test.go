package customer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// TestMapDomainError checks what a client actually receives, not just the
// status: the wrapped chain a service returns must never reach the wire, and
// an unrecognised error must stay generic.
//
// This is an internal test because mapDomainError is unexported; the rendering
// goes through Echo's error handler so the assertion is on the real body.
func TestMapDomainError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "not found, wrapped by every layer it passed through",
			err:        fmt.Errorf("customer.Service.Update: find customer: %w", ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"message":"customer not found"}`,
		},
		{
			name:       "email conflict",
			err:        fmt.Errorf("customer.Service.Register: check email: %w", ErrEmailExists),
			wantStatus: http.StatusConflict,
			wantBody:   `{"message":"email already registered"}`,
		},
		{
			name:       "invalid birth date",
			err:        fmt.Errorf("customer.Service.Register: new customer: %w", ErrInvalidBirthDate),
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   `{"message":"birth date cannot be in the future"}`,
		},
		{
			name:       "bare sentinel maps the same way",
			err:        ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantBody:   `{"message":"customer not found"}`,
		},
		{
			name:       "unknown error stays generic",
			err:        errors.New("pgx: connection refused (10.0.0.4:5432)"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"message":"internal error"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)

			e.HTTPErrorHandler(mapDomainError(tt.err), c)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "customer.Service",
				"the internal call path must not reach the client")
		})
	}
}
