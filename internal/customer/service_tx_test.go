package customer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the R2/R3 regressions. Before the fix, Update and Remove wrote the
// aggregate through the pool while the outbox message went into a separate
// transaction — a failed commit rolled back the event but kept the row.

var errCommitFailed = errors.New("commit failed")

// recordingTx records the lifecycle calls a service makes on its transaction.
type recordingTx struct {
	pgx.Tx
	commitErr    error
	commitCalls  int
	rollbackCall int
}

func (t *recordingTx) Commit(_ context.Context) error {
	t.commitCalls++
	return t.commitErr
}

func (t *recordingTx) Rollback(_ context.Context) error {
	t.rollbackCall++
	return nil
}

func (t *recordingTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// recordingBeginner hands out a single recordingTx and counts Begin calls.
type recordingBeginner struct {
	tx         *recordingTx
	beginCalls int
}

func (b *recordingBeginner) Begin(_ context.Context) (pgx.Tx, error) {
	b.beginCalls++
	return b.tx, nil
}

// txRepo wraps mockRepo and records which transaction each write was handed.
type txRepo struct {
	*mockRepo
	updateTxs []pgx.Tx
	deleteTxs []pgx.Tx
}

func (r *txRepo) UpdateTx(ctx context.Context, tx pgx.Tx, c customer.Customer) error {
	r.updateTxs = append(r.updateTxs, tx)
	return r.mockRepo.UpdateTx(ctx, tx, c)
}

func (r *txRepo) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	r.deleteTxs = append(r.deleteTxs, tx)
	return r.mockRepo.DeleteTx(ctx, tx, id)
}

// failingOutboxStore fails the outbox insert, forcing a rollback.
type failingOutboxStore struct {
	noopOutboxStore
	err error
}

func (s *failingOutboxStore) SaveTx(_ context.Context, _ pgx.Tx, _ outbox.OutboxMessage) error {
	return s.err
}

func newTxService(repo customer.Repository, db customer.Beginner, ob outbox.OutboxStore) *customer.Service {
	return customer.NewService(repo, db, ob, &noopEventStore{}, cache.NoopCache{}, discardLogger())
}

func seededTxRepo(t *testing.T) (*txRepo, customer.Customer) {
	t.Helper()
	inner := newMockRepo()
	c, err := customer.New("Alice", "alice@example.com", time.Now().AddDate(-30, 0, 0))
	require.NoError(t, err)
	inner.seed(c)
	return &txRepo{mockRepo: inner}, c
}

func TestService_Update_WritesInsideTransaction(t *testing.T) {
	t.Parallel()

	repo, c := seededTxRepo(t)
	tx := &recordingTx{commitErr: errCommitFailed}
	db := &recordingBeginner{tx: tx}

	svc := newTxService(repo, db, &noopOutboxStore{})
	err := svc.Update(context.Background(), customer.UpdateCmd{
		ID:        c.ID,
		Name:      "Alice Updated",
		Email:     c.Email,
		BirthDate: c.BirthDate,
	})

	require.ErrorIs(t, err, errCommitFailed)
	require.Len(t, repo.updateTxs, 1, "the customer row must be written exactly once")
	assert.Same(t, tx, repo.updateTxs[0], "the write must land on the service's transaction")
	assert.Equal(t, 1, tx.commitCalls)
	assert.Positive(t, tx.rollbackCall, "a failed commit must still be rolled back")
}

func TestService_Remove_RollsBackBothWrites(t *testing.T) {
	t.Parallel()

	repo, c := seededTxRepo(t)
	tx := &recordingTx{}
	db := &recordingBeginner{tx: tx}
	ob := &failingOutboxStore{err: errors.New("outbox insert failed")}

	svc := newTxService(repo, db, ob)
	err := svc.Remove(context.Background(), c.ID)

	require.Error(t, err)
	require.Len(t, repo.deleteTxs, 1)
	assert.Same(t, tx, repo.deleteTxs[0], "the delete must land on the service's transaction")
	assert.Zero(t, tx.commitCalls, "a failed outbox insert must not commit the delete")
	assert.Positive(t, tx.rollbackCall)
}

func TestService_Update_InvalidInput_OpensNoTransaction(t *testing.T) {
	t.Parallel()

	repo, c := seededTxRepo(t)
	db := &recordingBeginner{tx: &recordingTx{}}

	svc := newTxService(repo, db, &noopOutboxStore{})
	err := svc.Update(context.Background(), customer.UpdateCmd{
		ID:        c.ID,
		Name:      "Alice",
		Email:     c.Email,
		BirthDate: time.Now().AddDate(0, 0, 1),
	})

	require.ErrorIs(t, err, customer.ErrInvalidBirthDate)
	assert.Zero(t, db.beginCalls, "domain validation must fail before Begin")
	assert.Empty(t, repo.updateTxs)
}
