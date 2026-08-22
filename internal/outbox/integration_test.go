//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// SKIP LOCKED semantics cannot be stubbed meaningfully — these run against a
// real Postgres. They are the R4/R5 regressions: the previous implementation
// issued a bare SELECT ... FOR UPDATE through pool.Query, whose implicit
// single-statement transaction released the locks as soon as the rows came
// back, so two pollers claimed the same messages.

// _maxAttempts is declared in poller_test.go, which compiles under this tag too.
const _reclaimWindow = 5 * time.Minute

func newTestPool(t *testing.T) *pgxpool.Pool {
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

	pool, err := database.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB, err := goose.OpenDBWithDriver("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, goose.Up(sqlDB, "../../migrations"))

	return pool
}

// seedMessages inserts n unprocessed messages and returns their IDs.
func seedMessages(t *testing.T, pool *pgxpool.Pool, store outbox.OutboxStore, n int) []uuid.UUID {
	t.Helper()

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	ids := make([]uuid.UUID, n)
	for i := range n {
		ids[i] = uuid.New()
		require.NoError(t, store.SaveTx(ctx, tx, outbox.OutboxMessage{
			ID:          ids[i],
			AggregateID: uuid.New(),
			EventType:   "CustomerRegistered",
			Payload:     []byte(`{}`),
			// Stagger created_at so ORDER BY created_at is deterministic.
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
		}))
	}
	require.NoError(t, tx.Commit(ctx))
	return ids
}

func idSet(msgs []outbox.OutboxMessage) map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(msgs))
	for _, m := range msgs {
		set[m.ID] = true
	}
	return set
}

func TestPostgresOutboxStore_ConcurrentClaim(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	// Two independent store instances stand in for two poller replicas.
	storeA := outbox.NewPostgresStore(pool)
	storeB := outbox.NewPostgresStore(pool)

	const total = 10
	seedMessages(t, pool, storeA, total)

	type claim struct {
		msgs []outbox.OutboxMessage
		err  error
	}
	results := make(chan claim, 2)
	start := make(chan struct{})

	for _, s := range []outbox.OutboxStore{storeA, storeB} {
		go func() {
			<-start
			msgs, err := s.ClaimBatch(ctx, total, _reclaimWindow, _maxAttempts)
			results <- claim{msgs: msgs, err: err}
		}()
	}
	close(start)

	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)

	firstIDs, secondIDs := idSet(first.msgs), idSet(second.msgs)

	for id := range firstIDs {
		assert.False(t, secondIDs[id], "message %s was claimed by both pollers", id)
	}
	assert.Len(t, first.msgs, len(firstIDs), "a claim returned the same message twice")
	assert.Equal(t, total, len(firstIDs)+len(secondIDs),
		"every seeded message must be claimed exactly once")
}

func TestPostgresOutboxStore_AbandonedLockReclaimed(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	store := outbox.NewPostgresStore(pool)

	ids := seedMessages(t, pool, store, 1)

	claimed, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, ids[0], claimed[0].ID)

	// A live lock is not reclaimable.
	again, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	assert.Empty(t, again, "a message claimed moments ago must not be re-claimed")

	// Simulate the poller dying mid-flight: the lock ages past the window.
	_, err = pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = now() - interval '10 minutes' WHERE id = $1`,
		ids[0])
	require.NoError(t, err)

	reclaimed, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1, "an abandoned lock must become claimable again")
	assert.Equal(t, ids[0], reclaimed[0].ID)
}

func TestPostgresOutboxStore_MarkFailedReleasesClaim(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	store := outbox.NewPostgresStore(pool)

	ids := seedMessages(t, pool, store, 1)

	claimed, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, store.MarkFailed(ctx, ids[0], "downstream unavailable", 0))

	// No waiting for the reclaim window: MarkFailed clears locked_at.
	retry, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	require.Len(t, retry, 1, "a failed message must be retried on the next tick")
	assert.Equal(t, ids[0], retry[0].ID)
	assert.Equal(t, 1, retry[0].Attempts)
	require.NotNil(t, retry[0].LastError)
	assert.Equal(t, "downstream unavailable", *retry[0].LastError)
}

func TestPostgresOutboxStore_MarkProcessedRemovesFromClaims(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	store := outbox.NewPostgresStore(pool)

	ids := seedMessages(t, pool, store, 1)

	claimed, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, store.MarkProcessed(ctx, ids[0]))

	_, err = pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = now() - interval '10 minutes' WHERE id = $1`,
		ids[0])
	require.NoError(t, err)

	after, err := store.ClaimBatch(ctx, 1, _reclaimWindow, _maxAttempts)
	require.NoError(t, err)
	assert.Empty(t, after, "a processed message must never be claimed again")
}
