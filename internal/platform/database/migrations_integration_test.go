//go:build integration

package database_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const _migrationsDir = "../../../migrations"

func newMigrationDB(t *testing.T) *sql.DB {
	t.Helper()

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

	db, err := goose.OpenDBWithDriver("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = $1)`, name,
	).Scan(&exists))
	return exists
}

// TestMigration007_DropsAndRestoresEventLog covers the migration both ways.
// Down matters as much as Up here: the table is dropped, not emptied, so a
// rollback has to put back the exact schema the previous binary expects.
func TestMigration007_DropsAndRestoresEventLog(t *testing.T) {
	db := newMigrationDB(t)

	// Up to 006: the orphaned table is present, as it has been on every boot.
	require.NoError(t, goose.UpTo(db, _migrationsDir, 6))
	require.True(t, tableExists(t, db, "event_log"),
		"migration 003 must have created the table this spec removes")

	// Up to 007: dropped.
	require.NoError(t, goose.UpTo(db, _migrationsDir, 7))
	assert.False(t, tableExists(t, db, "event_log"),
		"007 must drop the orphaned Postgres event_log")

	// The tables that are actually used must be untouched.
	assert.True(t, tableExists(t, db, "customers"))
	assert.True(t, tableExists(t, db, "users"))
	assert.True(t, tableExists(t, db, "outbox_messages"))

	// Down: restored, with its index, so a rollback is safe.
	require.NoError(t, goose.DownTo(db, _migrationsDir, 6))
	require.True(t, tableExists(t, db, "event_log"),
		"the Down must recreate the table exactly as 003 defined it")

	var indexCount int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM pg_indexes
		 WHERE schemaname = 'public' AND indexname = 'idx_event_log_aggregate_id'`,
	).Scan(&indexCount))
	assert.Equal(t, 1, indexCount, "the Down must recreate the index too")

	// And forward again, so the migration is not one-way after a rollback.
	require.NoError(t, goose.UpTo(db, _migrationsDir, 7))
	assert.False(t, tableExists(t, db, "event_log"))
}

// TestMigrations_UpDownUp exercises the whole chain, which is the cheapest
// guard against a Down that does not undo its Up.
func TestMigrations_UpDownUp(t *testing.T) {
	db := newMigrationDB(t)

	require.NoError(t, goose.Up(db, _migrationsDir))
	require.NoError(t, goose.DownTo(db, _migrationsDir, 0))
	require.NoError(t, goose.Up(db, _migrationsDir))

	assert.True(t, tableExists(t, db, "customers"))
	assert.True(t, tableExists(t, db, "users"))
	assert.True(t, tableExists(t, db, "outbox_messages"))
	assert.False(t, tableExists(t, db, "event_log"),
		"a full replay must end with the orphaned table absent")
}
