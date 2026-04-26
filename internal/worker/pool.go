package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// Job is a unit of work executed by the pool.
type Job func(ctx context.Context) error

// Pool is a bounded goroutine pool. Workers are started on New and stopped
// when the context is cancelled or Stop is called. Stop blocks until all
// in-flight jobs complete — no work is dropped.
type Pool struct {
	jobs   chan Job
	wg     sync.WaitGroup
	logger *slog.Logger
}

// ErrPoolFull is returned by Submit when the job queue is at capacity.
var ErrPoolFull = errors.New("worker pool queue is full")

// New creates and starts a pool with the given number of workers.
// The pool is bound to ctx — cancelling ctx triggers a graceful shutdown.
// wg.Add(1) is called before launching each goroutine to guarantee the
// WaitGroup counter is incremented before any goroutine can call Done.
func New(ctx context.Context, workers int, queueSize int, logger *slog.Logger) *Pool {
	p := &Pool{
		jobs:   make(chan Job, queueSize),
		logger: logger,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.run(ctx)
	}
	return p
}

func (p *Pool) run(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case job, ok := <-p.jobs:
			if !ok {
				return // channel closed — Stop was called
			}
			if err := job(ctx); err != nil {
				p.logger.ErrorContext(ctx, "worker job failed", slog.Any("error", err))
			}
		case <-ctx.Done():
			return
		}
	}
}

// Submit enqueues a job. Returns ErrPoolFull if the queue is at capacity.
// Submit is non-blocking — callers must handle backpressure.
func (p *Pool) Submit(job Job) error {
	select {
	case p.jobs <- job:
		return nil
	default:
		return ErrPoolFull
	}
}

// Stop closes the job channel and waits for all workers to finish.
// Call this in the graceful shutdown sequence after context cancellation.
func (p *Pool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}
