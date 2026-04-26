// Package worker provides a bounded goroutine pool for background processing.
//
// The pool is started with a fixed number of workers and a buffered job queue.
// Workers read jobs from the queue and execute them. When the context is
// cancelled or Stop is called, workers finish any in-flight job before exiting —
// no work is dropped silently.
//
// Callers that need backpressure control should check for ErrPoolFull on Submit
// and decide whether to retry, queue locally, or drop the job.
package worker
