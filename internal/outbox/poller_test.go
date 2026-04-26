package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nopLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- test doubles ----

type stubStore struct {
	mu            sync.Mutex
	msgs          []outbox.OutboxMessage
	processedIDs  []uuid.UUID
	processedSet  map[uuid.UUID]bool
	failedIDs     []uuid.UUID
	failedReasons []string
}

func newStubStore(msgs ...outbox.OutboxMessage) *stubStore {
	return &stubStore{msgs: msgs, processedSet: map[uuid.UUID]bool{}}
}

func (s *stubStore) SaveTx(_ context.Context, _ pgx.Tx, msg outbox.OutboxMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *stubStore) FetchUnprocessed(_ context.Context, limit int) ([]outbox.OutboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending []outbox.OutboxMessage
	for _, m := range s.msgs {
		if !s.processedSet[m.ID] {
			pending = append(pending, m)
		}
	}
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (s *stubStore) MarkProcessed(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processedIDs = append(s.processedIDs, id)
	s.processedSet[id] = true
	return nil
}

func (s *stubStore) MarkFailed(_ context.Context, id uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedIDs = append(s.failedIDs, id)
	s.failedReasons = append(s.failedReasons, reason)
	// mark as processed so subsequent ticks don't re-deliver
	s.processedSet[id] = true
	return nil
}

type stubPublisher struct {
	mu       sync.Mutex
	published []outbox.OutboxMessage
	err      error
}

func (p *stubPublisher) Publish(_ context.Context, msg outbox.OutboxMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, msg)
	return nil
}

func newPoller(store outbox.OutboxStore, pub outbox.Publisher, pool *worker.Pool) *outbox.Poller {
	return outbox.NewPoller(store, pub, pool, 1*time.Millisecond, 50, nopLogger())
}

// ---- tests ----

func TestPoller_HappyPath(t *testing.T) {
	ctx := context.Background()

	msgID := uuid.New()
	store := newStubStore(outbox.OutboxMessage{
		ID: msgID, AggregateID: uuid.New(), EventType: "CustomerRegistered", Payload: []byte(`{}`), CreatedAt: time.Now(),
	})
	pub := &stubPublisher{}
	pool := worker.New(ctx, 2, 10, nopLogger())

	poller := newPoller(store, pub, pool)
	pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		poller.Run(pollCtx)
		close(done)
	}()

	<-pollCtx.Done()
	<-done
	pool.Stop() // wait for all in-flight deliver jobs to finish before asserting

	pub.mu.Lock()
	assert.Len(t, pub.published, 1)
	pub.mu.Unlock()

	store.mu.Lock()
	require.Len(t, store.processedIDs, 1)
	assert.Equal(t, msgID, store.processedIDs[0])
	store.mu.Unlock()
}

func TestPoller_PublishFails_MarkedFailed(t *testing.T) {
	ctx := context.Background()

	msgID := uuid.New()
	store := newStubStore(outbox.OutboxMessage{
		ID: msgID, AggregateID: uuid.New(), EventType: "CustomerRegistered", Payload: []byte(`{}`), CreatedAt: time.Now(),
	})
	pub := &stubPublisher{err: errors.New("downstream unavailable")}
	pool := worker.New(ctx, 2, 10, nopLogger())

	poller := newPoller(store, pub, pool)
	pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		poller.Run(pollCtx)
		close(done)
	}()

	<-pollCtx.Done()
	<-done
	pool.Stop() // wait for all in-flight deliver jobs to finish before asserting

	store.mu.Lock()
	assert.Empty(t, store.processedIDs)
	require.Len(t, store.failedIDs, 1)
	assert.Equal(t, msgID, store.failedIDs[0])
	store.mu.Unlock()
}

func TestPoller_PoolFull_BatchSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1 worker, queue size 1
	pool := worker.New(ctx, 1, 1, nopLogger())

	block := make(chan struct{})
	workerBusy := make(chan struct{}) // closed once the worker starts executing job1

	// Job1: signal it started, then block — keeps the worker occupied.
	require.NoError(t, pool.Submit(func(ctx context.Context) error {
		close(workerBusy)
		<-block
		return nil
	}))
	<-workerBusy // worker is executing job1; the queue is now empty

	// Job2: fills the queue. Any further Submit will return ErrPoolFull.
	require.NoError(t, pool.Submit(func(ctx context.Context) error { <-block; return nil }))

	store := newStubStore(
		outbox.OutboxMessage{ID: uuid.New(), AggregateID: uuid.New(), EventType: "CustomerRegistered", Payload: []byte(`{}`), CreatedAt: time.Now()},
		outbox.OutboxMessage{ID: uuid.New(), AggregateID: uuid.New(), EventType: "CustomerUpdated", Payload: []byte(`{}`), CreatedAt: time.Now()},
	)
	pub := &stubPublisher{}
	poller := newPoller(store, pub, pool)

	// Pool is deterministically full; poller must skip the batch and log a warning.
	pollCtx, cancelPoll := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelPoll()

	done := make(chan struct{})
	go func() {
		poller.Run(pollCtx)
		close(done)
	}()

	<-done

	close(block)
	pool.Stop()

	pub.mu.Lock()
	assert.Empty(t, pub.published)
	pub.mu.Unlock()
}

func TestPoller_EmptyOutbox_NoMarkCalls(t *testing.T) {
	ctx := context.Background()

	store := newStubStore() // empty
	pub := &stubPublisher{}
	pool := worker.New(ctx, 2, 10, nopLogger())

	poller := newPoller(store, pub, pool)
	pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		poller.Run(pollCtx)
		close(done)
	}()

	<-pollCtx.Done()
	<-done
	pool.Stop()

	store.mu.Lock()
	assert.Empty(t, store.processedIDs)
	assert.Empty(t, store.failedIDs)
	store.mu.Unlock()
}
