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

	p.Stop()
	assert.Equal(t, int64(5), count.Load())
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

func TestPool_ContextCancelledStopsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := worker.New(ctx, 3, 10, nopLogger())

	var count atomic.Int64
	done := make(chan struct{})

	err := p.Submit(func(ctx context.Context) error {
		defer close(done)
		count.Add(1)
		return nil
	})
	require.NoError(t, err)

	<-done // in-flight job finished
	cancel()
	p.Stop() // must not deadlock

	assert.Equal(t, int64(1), count.Load())
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
