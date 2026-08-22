// Package worker provides a bounded goroutine pool for background processing.
//
// The pool is started with a fixed number of workers and a buffered job queue.
// Workers read jobs from the queue and execute them until Stop closes it.
//
// Stop is mandatory: workers do not exit on context cancellation, so a pool
// that is never stopped leaks its goroutines. In exchange, Stop drains the
// whole queue — jobs submitted before shutdown began still run.
//
// The context handed to New is the job context, not the lifecycle context.
// It must stay live across shutdown so drained jobs can still reach the network
// and the database; see New for the WithoutCancel pattern callers use.
//
// Callers that need backpressure control should check for ErrPoolFull on Submit
// and decide whether to retry, queue locally, or drop the job.
package worker
