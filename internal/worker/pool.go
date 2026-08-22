package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Job is a unit of work executed by the pool.
type Job func(ctx context.Context) error

// Pool is a bounded goroutine pool. Workers are started on New and run until
// Stop closes the job queue. Stop drains every job already queued before
// returning — no work is dropped.
type Pool struct {
	jobs     chan Job
	wg       sync.WaitGroup
	executed atomic.Int64
	logger   *slog.Logger
}

// ErrPoolFull is returned by Submit when the job queue is at capacity.
var ErrPoolFull = errors.New("worker pool queue is full")

// New creates and starts a pool with the given number of workers.
//
// jobCtx is passed to every job and must NOT be the lifecycle context: workers
// exit only when Stop closes the queue, so a jobCtx cancelled by the shutdown
// signal would make every drained job fail at its first network or database
// call. Callers shutting down on a signal derive jobCtx with
// context.WithoutCancel plus a drain timeout.
//
// Stop is mandatory, not optional — the worker goroutines leak without it.
//
// wg.Add(1) is called before launching each goroutine to guarantee the
// WaitGroup counter is incremented before any goroutine can call Done.
func New(jobCtx context.Context, workers int, queueSize int, logger *slog.Logger) *Pool {
	p := &Pool{
		jobs:   make(chan Job, queueSize),
		logger: logger,
	}
	for range workers {
		p.wg.Add(1)
		go p.run(jobCtx)
	}
	return p
}

// run drains p.jobs until Stop closes it. Closing the channel is deliberately
// the only exit path: returning on context cancellation instead would discard
// whatever is still queued, which is exactly the guarantee Stop makes.
func (p *Pool) run(jobCtx context.Context) {
	defer p.wg.Done()
	for job := range p.jobs {
		if err := job(jobCtx); err != nil {
			p.logger.ErrorContext(jobCtx, "worker job failed", slog.Any("error", err))
		}
		p.executed.Add(1)
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

// Stop closes the job channel and waits for all workers to finish. Every job
// already queued runs to completion first. It reports how many jobs finished
// during the drain, for the shutdown log in main.
//
// Call this in the graceful shutdown sequence after context cancellation.
// Stop must be called exactly once; a second call panics on the closed channel.
func (p *Pool) Stop() int {
	before := p.executed.Load()
	close(p.jobs)
	p.wg.Wait()
	return int(p.executed.Load() - before)
}
