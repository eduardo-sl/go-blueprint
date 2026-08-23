package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetrytest"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// _maxAttempts is the delivery ceiling these tests poll under.
const _maxAttempts = 5

// _pollInterval doubles as the backoff base, so the expected schedules below
// are all multiples of it.
const _pollInterval = 1 * time.Millisecond

func nopLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// safeBuffer collects log output. Delivery runs on a worker goroutine, so the
// buffer is written from a goroutine other than the one asserting on it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---- test doubles ----

type stubStore struct {
	mu               sync.Mutex
	msgs             []outbox.OutboxMessage
	claimed          map[uuid.UUID]bool
	processedIDs     []uuid.UUID
	processedSet     map[uuid.UUID]bool
	failedIDs        []uuid.UUID
	failedReasons    []string
	failedRetryAfter []time.Duration
	exhaustedIDs     []uuid.UUID
	exhaustedReasons []string
}

func newStubStore(msgs ...outbox.OutboxMessage) *stubStore {
	return &stubStore{
		msgs:         msgs,
		claimed:      map[uuid.UUID]bool{},
		processedSet: map[uuid.UUID]bool{},
	}
}

func (s *stubStore) SaveTx(_ context.Context, _ pgx.Tx, msg outbox.OutboxMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}

// ClaimBatch mirrors the real store: a message already claimed is not handed
// out again until MarkFailed releases it.
func (s *stubStore) ClaimBatch(_ context.Context, limit int, _ time.Duration, maxAttempts int) ([]outbox.OutboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending []outbox.OutboxMessage
	for _, m := range s.msgs {
		if !s.processedSet[m.ID] && !s.claimed[m.ID] && m.Attempts < maxAttempts {
			pending = append(pending, m)
		}
	}
	if len(pending) > limit {
		pending = pending[:limit]
	}
	for _, m := range pending {
		s.claimed[m.ID] = true
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

func (s *stubStore) MarkFailed(_ context.Context, id uuid.UUID, reason string, retryAfter time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedIDs = append(s.failedIDs, id)
	s.failedReasons = append(s.failedReasons, reason)
	s.failedRetryAfter = append(s.failedRetryAfter, retryAfter)
	// The real store clears locked_at here so the next tick retries. This stub
	// marks the message processed instead, to keep the failure count at one.
	s.processedSet[id] = true
	return nil
}

// MarkExhausted mirrors the real store: the row leaves the claim query for good.
func (s *stubStore) MarkExhausted(_ context.Context, id uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exhaustedIDs = append(s.exhaustedIDs, id)
	s.exhaustedReasons = append(s.exhaustedReasons, reason)
	s.processedSet[id] = true
	return nil
}

type stubPublisher struct {
	mu        sync.Mutex
	published []outbox.OutboxMessage
	err       error
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
	return outbox.NewPoller(store, pub, pool, _pollInterval, 50, _maxAttempts, nopLogger())
}

// runPoller runs p until ctx expires and drains the worker pool, so every
// deliver job has finished before the caller asserts.
func runPoller(ctx context.Context, p *outbox.Poller, pool *worker.Pool) {
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	<-ctx.Done()
	<-done
	pool.Stop()
}

func failingMsg(attempts int) outbox.OutboxMessage {
	return outbox.OutboxMessage{
		ID:          uuid.New(),
		AggregateID: uuid.New(),
		EventType:   "CustomerRegistered",
		Payload:     []byte(`{}`),
		CreatedAt:   time.Now(),
		Attempts:    attempts,
	}
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

// TestPoller_FailureSchedulesBackoff pins the retry schedule: the delay doubles
// with each recorded attempt and stops growing at five minutes, so an outage
// spaces retries out instead of letting the poller spin on them.
func TestPoller_FailureSchedulesBackoff(t *testing.T) {
	tests := []struct {
		name           string
		attempts       int
		wantRetryAfter time.Duration
	}{
		{name: "first failure", attempts: 0, wantRetryAfter: 2 * _pollInterval},
		{name: "second failure", attempts: 1, wantRetryAfter: 4 * _pollInterval},
		{name: "fourth failure", attempts: 3, wantRetryAfter: 16 * _pollInterval},
		{name: "capped, not overflowed", attempts: 20, wantRetryAfter: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			msg := failingMsg(tt.attempts)
			store := newStubStore(msg)
			pub := &stubPublisher{err: errors.New("downstream unavailable")}
			pool := worker.New(ctx, 2, 10, nopLogger())

			// A ceiling far above tt.attempts keeps every case on the retry
			// path; exhaustion is covered separately below.
			poller := outbox.NewPoller(store, pub, pool, _pollInterval, 50, 1_000, nopLogger())
			pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()

			runPoller(pollCtx, poller, pool)

			store.mu.Lock()
			defer store.mu.Unlock()
			require.Len(t, store.failedRetryAfter, 1)
			assert.Equal(t, tt.wantRetryAfter, store.failedRetryAfter[0])
			assert.Empty(t, store.exhaustedIDs)
		})
	}
}

// TestPoller_ExhaustedRetries_Terminal covers the one place a message stops
// being retried: the last attempt must retire the row rather than reschedule
// it, and must say so at error level.
func TestPoller_ExhaustedRetries_Terminal(t *testing.T) {
	ctx := context.Background()

	msg := failingMsg(_maxAttempts - 1)
	store := newStubStore(msg)
	pub := &stubPublisher{err: errors.New("downstream unavailable")}
	pool := worker.New(ctx, 2, 10, nopLogger())

	logs := &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelError}))

	poller := outbox.NewPoller(store, pub, pool, _pollInterval, 50, _maxAttempts, logger)
	pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	runPoller(pollCtx, poller, pool)

	store.mu.Lock()
	require.Len(t, store.exhaustedIDs, 1)
	assert.Equal(t, msg.ID, store.exhaustedIDs[0])
	assert.Equal(t, "downstream unavailable", store.exhaustedReasons[0])
	assert.Empty(t, store.failedIDs, "an exhausted message must not be rescheduled")
	assert.Empty(t, store.processedIDs, "the message was never delivered")
	store.mu.Unlock()

	out := logs.String()
	assert.Contains(t, out, "outbox message exhausted retries")
	assert.Contains(t, out, msg.ID.String())
}

// TestPoller_ExhaustedMessageNotReclaimed guards the loop the attempt ceiling
// exists to break: once retired, the message must not come back on a later tick.
func TestPoller_ExhaustedMessageNotReclaimed(t *testing.T) {
	ctx := context.Background()

	msg := failingMsg(_maxAttempts - 1)
	store := newStubStore(msg)
	pub := &stubPublisher{err: errors.New("downstream unavailable")}
	pool := worker.New(ctx, 2, 10, nopLogger())

	poller := newPoller(store, pub, pool)
	// Long enough for many ticks at a 1ms interval.
	pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	runPoller(pollCtx, poller, pool)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Len(t, store.exhaustedIDs, 1, "the message was delivered to the terminal path more than once")
}

// TestPoller_Metrics pins the publish-outcome counters to the branches that
// drive them. Both instruments were registered and exported long before
// anything incremented them, so a test that the instrument exists proves
// nothing — this asserts the recorded value at each outcome.
//
// Not parallel: CollectCounters swaps the global meter provider.
func TestPoller_Metrics(t *testing.T) {
	tests := []struct {
		name          string
		publishErr    error
		attempts      int
		maxAttempts   int
		wantPublished int64
		wantFailures  int64
	}{
		{
			name:          "success counts a publish",
			attempts:      0,
			maxAttempts:   _maxAttempts,
			wantPublished: 1,
			wantFailures:  0,
		},
		{
			name:          "failure counts a failure",
			publishErr:    errors.New("downstream unavailable"),
			attempts:      0,
			maxAttempts:   _maxAttempts,
			wantPublished: 0,
			wantFailures:  1,
		},
		{
			// The exhaustion branch must count too: it is still a failed
			// delivery, and it is the one that drops the event for good.
			name:          "exhaustion counts a failure",
			publishErr:    errors.New("downstream unavailable"),
			attempts:      _maxAttempts - 1,
			maxAttempts:   _maxAttempts,
			wantPublished: 0,
			wantFailures:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			msg := failingMsg(tt.attempts)
			store := newStubStore(msg)
			pub := &stubPublisher{err: tt.publishErr}
			pool := worker.New(ctx, 2, 10, nopLogger())

			poller := outbox.NewPoller(store, pub, pool, _pollInterval, 50, tt.maxAttempts, nopLogger())
			pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()

			counters := telemetrytest.CollectCounters(t, func() {
				runPoller(pollCtx, poller, pool)
			})

			assert.Equal(t, tt.wantPublished, counters.Counter("outbox.messages.published.total"),
				"outbox.messages.published.total")
			assert.Equal(t, tt.wantFailures, counters.Counter("outbox.publish.failures.total"),
				"outbox.publish.failures.total")
		})
	}
}
