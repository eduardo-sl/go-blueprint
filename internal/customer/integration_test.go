//go:build integration

package customer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database"
	pgrepo "github.com/eduardo-sl/go-blueprint/internal/platform/database/postgres"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestCustomerRepository_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("blueprint_test"),
		tcpostgres.WithUsername("blueprint"),
		tcpostgres.WithPassword("blueprint"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := database.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB, err := goose.OpenDBWithDriver("pgx/v5", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, goose.Up(sqlDB, "../../migrations"))

	repo := pgrepo.NewCustomerRepository(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := customer.NewService(repo, &noopStore{}, cache.NoopCache{}, logger)
	query := customer.NewQueryService(repo)

	yesterday := time.Now().AddDate(0, 0, -1)

	t.Run("register and retrieve customer", func(t *testing.T) {
		id, err := svc.Register(ctx, customer.RegisterCmd{
			Name:      "Alice Integration",
			Email:     "alice.integration@example.com",
			BirthDate: yesterday,
		})
		require.NoError(t, err)

		c, err := query.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "Alice Integration", c.Name)
		assert.Equal(t, "alice.integration@example.com", c.Email)
		assert.Equal(t, id, c.ID)
	})

	t.Run("duplicate email returns ErrEmailExists", func(t *testing.T) {
		email := "duplicate.integration@example.com"
		_, err := svc.Register(ctx, customer.RegisterCmd{
			Name:      "First",
			Email:     email,
			BirthDate: yesterday,
		})
		require.NoError(t, err)

		_, err = svc.Register(ctx, customer.RegisterCmd{
			Name:      "Second",
			Email:     email,
			BirthDate: yesterday,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, customer.ErrEmailExists))
	})

	t.Run("update customer", func(t *testing.T) {
		id, err := svc.Register(ctx, customer.RegisterCmd{
			Name:      "Bob",
			Email:     "bob.integration@example.com",
			BirthDate: yesterday,
		})
		require.NoError(t, err)

		err = svc.Update(ctx, customer.UpdateCmd{
			ID:        id,
			Name:      "Bob Updated",
			Email:     "bob.updated@example.com",
			BirthDate: yesterday,
		})
		require.NoError(t, err)

		c, err := query.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "Bob Updated", c.Name)
		assert.Equal(t, "bob.updated@example.com", c.Email)
	})

	t.Run("remove customer", func(t *testing.T) {
		id, err := svc.Register(ctx, customer.RegisterCmd{
			Name:      "Charlie",
			Email:     "charlie.integration@example.com",
			BirthDate: yesterday,
		})
		require.NoError(t, err)

		err = svc.Remove(ctx, id)
		require.NoError(t, err)

		_, err = query.GetByID(ctx, id)
		assert.True(t, errors.Is(err, customer.ErrNotFound))
	})

	t.Run("list returns registered customers", func(t *testing.T) {
		customers, err := query.List(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, customers)
	})

	t.Run("remove non-existent customer returns ErrNotFound", func(t *testing.T) {
		err := svc.Remove(ctx, uuid.New())
		assert.True(t, errors.Is(err, customer.ErrNotFound))
	})
}
