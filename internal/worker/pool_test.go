package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPool_SubmitWithinCapacity(t *testing.T) {
	ctx := context.Background()
	p := worker.New(ctx, 2, 10, nopLogger())

	var count atomic.Int64
	for i := 0; i < 5; i++ {
		err := p.Submit(func(ctx context.Context) error {
			count.Add(1)
			return nil
		})
		require.NoError(t, err)
	}

	drained := p.Stop()
	assert.Equal(t, int64(5), count.Load())
	assert.Equal(t, 5, drained, "Stop reports the jobs it drained")
}

func TestPool_SubmitOverCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1 worker, queue size 1 — easy to fill
	p := worker.New(ctx, 1, 1, nopLogger())

	block := make(chan struct{})
	// Fill the worker and queue
	_ = p.Submit(func(ctx context.Context) error { <-block; return nil })
	_ = p.Submit(func(ctx context.Context) error { <-block; return nil })

	// Next submit must be rejected immediately (non-blocking)
	err := p.Submit(func(ctx context.Context) error { return nil })
	assert.ErrorIs(t, err, worker.ErrPoolFull)

	close(block)
	p.Stop()
}

// TestPool_QueuedJobsSurviveShutdown is the R6 regression. It deliberately
// passes the cancelled context straight to New — the way main used to wire the
// pool — because that is the shape the defect needed: with a ctx.Done() case in
// run, the workers return the moment the context dies and everything still
// buffered in the queue is silently dropped. Stop's contract is that the queue
// drains regardless of what happened to the context it was given.
func TestPool_QueuedJobsSurviveShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := worker.New(ctx, 1, 10, nopLogger())

	var count atomic.Int64
	release := make(chan struct{})
	for range 10 {
		require.NoError(t, p.Submit(func(context.Context) error {
			<-release // hold the single worker until every job is queued
			count.Add(1)
			return nil
		}))
	}

	cancel()
	close(release)

	drained := p.Stop()

	assert.Equal(t, int64(10), count.Load(), "queued jobs were dropped on shutdown")
	assert.Equal(t, 10, drained)
}

// TestPool_DrainedJobHasLiveContext is the R7 regression: it pins the wiring
// main uses. The pool hands jobs exactly the context New was given, so deriving
// it with WithoutCancel is what keeps a drained outbox delivery able to reach
// Kafka and Postgres after the signal. Draining into the cancelled lifecycle
// context would make every drained job fail at its first call.
func TestPool_DrainedJobHasLiveContext(t *testing.T) {
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())

	jobCtx, cancelJobs := context.WithTimeout(
		context.WithoutCancel(lifecycle), 5*time.Second,
	)
	defer cancelJobs()

	p := worker.New(jobCtx, 1, 10, nopLogger())

	release := make(chan struct{})
	observed := make(chan error, 1)
	require.NoError(t, p.Submit(func(ctx context.Context) error {
		<-release
		observed <- ctx.Err()
		return nil
	}))

	cancelLifecycle()
	close(release)
	p.Stop()

	require.NoError(t, <-observed, "drained job ran with a cancelled context")
}

func TestPool_JobError_PoolContinues(t *testing.T) {
	ctx := context.Background()
	p := worker.New(ctx, 1, 10, nopLogger())

	var good atomic.Int64
	errJob := func(ctx context.Context) error { return errors.New("boom") }
	goodJob := func(ctx context.Context) error { good.Add(1); return nil }

	require.NoError(t, p.Submit(errJob))
	require.NoError(t, p.Submit(goodJob))
	require.NoError(t, p.Submit(goodJob))

	p.Stop()
	assert.Equal(t, int64(2), good.Load())
}

func TestPool_StopAfterCancel_NoDealock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	p := worker.New(ctx, 4, 20, nopLogger())

	for i := 0; i < 10; i++ {
		_ = p.Submit(func(ctx context.Context) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		})
	}

	<-ctx.Done()

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() deadlocked after context cancellation")
	}
}
