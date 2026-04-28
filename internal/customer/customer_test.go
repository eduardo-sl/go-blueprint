package customer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Parallel()

	yesterday := time.Now().AddDate(0, 0, -1)
	tomorrow := time.Now().AddDate(0, 0, 1)

	tests := []struct {
		name      string
		custName  string
		email     string
		birthDate time.Time
		wantErr   error
	}{
		{
			name:      "valid customer",
			custName:  "Alice",
			email:     "alice@example.com",
			birthDate: yesterday,
		},
		{
			name:      "trims whitespace and lowercases email",
			custName:  "  Bob  ",
			email:     "  BOB@EXAMPLE.COM  ",
			birthDate: yesterday,
		},
		{
			name:      "empty name",
			custName:  "",
			email:     "alice@example.com",
			birthDate: yesterday,
			wantErr:   customer.ErrNameRequired,
		},
		{
			name:      "whitespace-only name",
			custName:  "   ",
			email:     "alice@example.com",
			birthDate: yesterday,
			wantErr:   customer.ErrNameRequired,
		},
		{
			name:      "empty email",
			custName:  "Alice",
			email:     "",
			birthDate: yesterday,
			wantErr:   customer.ErrEmailRequired,
		},
		{
			name:      "future birth date",
			custName:  "Alice",
			email:     "alice@example.com",
			birthDate: tomorrow,
			wantErr:   customer.ErrInvalidBirthDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := customer.New(tt.custName, tt.email, tt.birthDate)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, c.ID)
			assert.NotEmpty(t, c.Name)
			assert.NotEmpty(t, c.Email)
			assert.False(t, c.CreatedAt.IsZero())
			assert.False(t, c.UpdatedAt.IsZero())
		})
	}
}

func TestCustomer_Update(t *testing.T) {
	t.Parallel()

	yesterday := time.Now().AddDate(0, 0, -1)

	tests := []struct {
		name      string
		newName   string
		newEmail  string
		birthDate time.Time
		wantErr   error
	}{
		{
			name:      "valid update",
			newName:   "Alice Smith",
			newEmail:  "alice.smith@example.com",
			birthDate: yesterday,
		},
		{
			name:      "empty name",
			newName:   "",
			newEmail:  "alice@example.com",
			birthDate: yesterday,
			wantErr:   customer.ErrNameRequired,
		},
		{
			name:      "future birth date",
			newName:   "Alice",
			newEmail:  "alice@example.com",
			birthDate: time.Now().AddDate(0, 0, 1),
			wantErr:   customer.ErrInvalidBirthDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := customer.New("Alice", "alice@example.com", yesterday)
			require.NoError(t, err)

			err = c.Update(tt.newName, tt.newEmail, tt.birthDate)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr))
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.newName, c.Name)
			assert.Equal(t, tt.newEmail, c.Email)
		})
	}
}

func TestService_Register(t *testing.T) {
	t.Parallel()

	yesterday := time.Now().AddDate(0, 0, -1)

	tests := []struct {
		name    string
		cmd     customer.RegisterCmd
		setup   func(*mockRepo)
		wantErr error
	}{
		{
			name: "success",
			cmd: customer.RegisterCmd{
				Name:      "Alice",
				Email:     "alice@example.com",
				BirthDate: yesterday,
			},
		},
		{
			name: "duplicate email",
			cmd: customer.RegisterCmd{
				Name:      "Alice",
				Email:     "existing@example.com",
				BirthDate: yesterday,
			},
			setup: func(r *mockRepo) {
				existing, _ := customer.New("Existing", "existing@example.com", yesterday)
				_ = r.Save(context.Background(), existing)
			},
			wantErr: customer.ErrEmailExists,
		},
		{
			name: "invalid birth date",
			cmd: customer.RegisterCmd{
				Name:      "Alice",
				Email:     "alice@example.com",
				BirthDate: time.Now().AddDate(0, 0, 1),
			},
			wantErr: customer.ErrInvalidBirthDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}

			svc := newTestService(repo)
			id, err := svc.Register(context.Background(), tt.cmd)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.NotEqual(t, uuid.Nil, id)
		})
	}
}

func TestService_Remove(t *testing.T) {
	t.Parallel()

	yesterday := time.Now().AddDate(0, 0, -1)

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		repo := newMockRepo()
		c, _ := customer.New("Alice", "alice@example.com", yesterday)
		_ = repo.Save(context.Background(), c)

		svc := newTestService(repo)
		err := svc.Remove(context.Background(), c.ID)
		require.NoError(t, err)

		_, err = repo.FindByID(context.Background(), c.ID)
		assert.True(t, errors.Is(err, customer.ErrNotFound))
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(newMockRepo())
		err := svc.Remove(context.Background(), uuid.New())
		assert.True(t, errors.Is(err, customer.ErrNotFound))
	})
}

// --- test helpers ---

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type noopEventStore struct{}

func (n *noopEventStore) Append(_ context.Context, _ eventlog.Event) error { return nil }
func (n *noopEventStore) FetchSince(_ context.Context, _ string, _ time.Time) ([]eventlog.Event, error) {
	return nil, nil
}

type noopOutboxStore struct{}

func (n *noopOutboxStore) SaveTx(_ context.Context, _ pgx.Tx, _ outbox.OutboxMessage) error {
	return nil
}
func (n *noopOutboxStore) FetchUnprocessed(_ context.Context, _ int) ([]outbox.OutboxMessage, error) {
	return nil, nil
}
func (n *noopOutboxStore) MarkProcessed(_ context.Context, _ uuid.UUID) error { return nil }
func (n *noopOutboxStore) MarkFailed(_ context.Context, _ uuid.UUID, _ string) error { return nil }

// stubTx is a no-op pgx.Tx for unit tests that do not touch a real database.
// Methods not implemented here panic — if a test triggers them, add the stub.
type stubTx struct{ pgx.Tx }

func (t *stubTx) Commit(_ context.Context) error   { return nil }
func (t *stubTx) Rollback(_ context.Context) error { return nil }
func (t *stubTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

type stubBeginner struct{}

func (b *stubBeginner) Begin(_ context.Context) (pgx.Tx, error) { return &stubTx{}, nil }

func newTestService(repo customer.Repository) *customer.Service {
	return customer.NewService(repo, &stubBeginner{}, &noopOutboxStore{}, &noopEventStore{}, cache.NoopCache{}, discardLogger())
}

type mockRepo struct {
	customers map[uuid.UUID]customer.Customer
	byEmail   map[string]customer.Customer
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		customers: make(map[uuid.UUID]customer.Customer),
		byEmail:   make(map[string]customer.Customer),
	}
}

func (m *mockRepo) Save(_ context.Context, c customer.Customer) error {
	m.customers[c.ID] = c
	m.byEmail[c.Email] = c
	return nil
}

func (m *mockRepo) SaveTx(_ context.Context, _ pgx.Tx, c customer.Customer) error {
	return m.Save(context.Background(), c)
}

func (m *mockRepo) Update(_ context.Context, c customer.Customer) error {
	m.customers[c.ID] = c
	m.byEmail[c.Email] = c
	return nil
}

func (m *mockRepo) Delete(_ context.Context, id uuid.UUID) error {
	c, ok := m.customers[id]
	if !ok {
		return customer.ErrNotFound
	}
	delete(m.customers, id)
	delete(m.byEmail, c.Email)
	return nil
}

func (m *mockRepo) FindByID(_ context.Context, id uuid.UUID) (customer.Customer, error) {
	c, ok := m.customers[id]
	if !ok {
		return customer.Customer{}, customer.ErrNotFound
	}
	return c, nil
}

func (m *mockRepo) FindByEmail(_ context.Context, email string) (customer.Customer, error) {
	c, ok := m.byEmail[email]
	if !ok {
		return customer.Customer{}, customer.ErrNotFound
	}
	return c, nil
}

func (m *mockRepo) List(_ context.Context) ([]customer.Customer, error) {
	result := make([]customer.Customer, 0, len(m.customers))
	for _, c := range m.customers {
		result = append(result, c)
	}
	return result, nil
}
