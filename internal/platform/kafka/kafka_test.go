package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/kafka"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newOutboxMsg(eventType string) outbox.OutboxMessage {
	return outbox.OutboxMessage{
		ID:          uuid.New(),
		AggregateID: uuid.New(),
		EventType:   eventType,
		Payload:     json.RawMessage(`{"test":true}`),
		CreatedAt:   time.Now().UTC(),
	}
}

// newFakeCluster creates a kfake cluster with pre-seeded topics.
// Session/heartbeat timeouts are shortened so consumer group tests run faster.
func newFakeCluster(t *testing.T, topics ...string) (*kfake.Cluster, []string) {
	t.Helper()
	opts := []kfake.Opt{
		kfake.NumBrokers(1),
		kfake.BrokerConfigs(map[string]string{
			"group.consumer.heartbeat.interval.ms": "100",
		}),
	}
	for _, topic := range topics {
		opts = append(opts, kfake.SeedTopics(1, topic))
	}
	fk, err := kfake.NewCluster(opts...)
	require.NoError(t, err)
	t.Cleanup(fk.Close)
	return fk, fk.ListenAddrs()
}

// TestProducer_HappyPath verifies a published record arrives with the correct key and headers.
func TestProducer_HappyPath(t *testing.T) {
	topic := "test.events.happy"
	dlqTopic := "test.events.happy.dlq"
	_, addrs := newFakeCluster(t, topic, dlqTopic)
	logger := newTestLogger()

	dlqWriter, err := kafka.NewDLQWriter(addrs, dlqTopic, logger)
	require.NoError(t, err)
	defer dlqWriter.Close()

	producer, err := kafka.NewProducer(addrs, topic, dlqWriter, 3, logger)
	require.NoError(t, err)
	defer producer.Close()

	msg := newOutboxMsg("CustomerRegistered")
	require.NoError(t, producer.Publish(context.Background(), msg))

	// Consume to verify the record arrived with expected metadata.
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(addrs...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err)
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fetches := cl.PollFetches(ctx)
	require.NoError(t, fetches.Err())

	var records []*kgo.Record
	fetches.EachRecord(func(r *kgo.Record) { records = append(records, r) })
	require.Len(t, records, 1)

	headers := make(map[string]string)
	for _, h := range records[0].Headers {
		headers[h.Key] = string(h.Value)
	}
	assert.Equal(t, msg.AggregateID.String(), string(records[0].Key))
	assert.Equal(t, "CustomerRegistered", headers["event_type"])
	assert.Equal(t, msg.ID.String(), headers["message_id"])
}

// TestDLQWriter_Write verifies the DLQ record carries failure metadata headers.
func TestDLQWriter_Write(t *testing.T) {
	dlqTopic := "test.events.dlq"
	_, addrs := newFakeCluster(t, dlqTopic)
	logger := newTestLogger()

	dlqWriter, err := kafka.NewDLQWriter(addrs, dlqTopic, logger)
	require.NoError(t, err)
	defer dlqWriter.Close()

	msg := newOutboxMsg("CustomerRegistered")
	writeErr := errors.New("upstream broker unreachable")

	require.NoError(t, dlqWriter.Write(context.Background(), msg, writeErr))

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(addrs...),
		kgo.ConsumeTopics(dlqTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err)
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fetches := cl.PollFetches(ctx)
	require.NoError(t, fetches.Err())

	var records []*kgo.Record
	fetches.EachRecord(func(r *kgo.Record) { records = append(records, r) })
	require.Len(t, records, 1)

	headers := make(map[string]string)
	for _, h := range records[0].Headers {
		headers[h.Key] = string(h.Value)
	}
	assert.Equal(t, msg.ID.String(), headers["message_id"])
	assert.Equal(t, writeErr.Error(), headers["failure_reason"])
	assert.NotEmpty(t, headers["failed_at"])
}

// TestConsumer_HappyPath verifies the handler is called for each consumed record.
func TestConsumer_HappyPath(t *testing.T) {
	topic := "test.events.consumer"
	_, addrs := newFakeCluster(t, topic)
	logger := newTestLogger()

	// Pre-produce a record via a plain kgo client.
	cl, err := kgo.NewClient(kgo.SeedBrokers(addrs...))
	require.NoError(t, err)
	defer cl.Close()

	err = cl.ProduceSync(context.Background(), &kgo.Record{
		Topic: topic,
		Value: []byte("payload"),
		Headers: []kgo.RecordHeader{
			{Key: "message_id", Value: []byte(uuid.New().String())},
			{Key: "event_type", Value: []byte("CustomerRegistered")},
		},
	}).FirstErr()
	require.NoError(t, err)

	var callCount atomic.Int32
	handler := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	consumer, err := kafka.NewConsumer(addrs, "test-group-happy", topic, handler, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return callCount.Load() >= 1
	}, 10*time.Second, 50*time.Millisecond, "handler must be called at least once")

	cancel()
	<-done
	consumer.Close()
}

// TestConsumer_HandlerError_OffsetNotCommitted verifies that a failed batch is
// redelivered on next consumer startup (offset not committed on error).
func TestConsumer_HandlerError_OffsetNotCommitted(t *testing.T) {
	topic := "test.events.retry"
	group := "retry-group"
	_, addrs := newFakeCluster(t, topic)
	logger := newTestLogger()

	// Pre-produce a record.
	cl, err := kgo.NewClient(kgo.SeedBrokers(addrs...))
	require.NoError(t, err)
	defer cl.Close()

	err = cl.ProduceSync(context.Background(), &kgo.Record{
		Topic: topic,
		Value: []byte("payload"),
		Headers: []kgo.RecordHeader{
			{Key: "message_id", Value: []byte(uuid.New().String())},
		},
	}).FirstErr()
	require.NoError(t, err)

	// First consumer: always returns an error (offset must NOT be committed).
	var firstCallCount atomic.Int32
	failHandler := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		firstCallCount.Add(1)
		return errors.New("transient error")
	})

	consumer1, err := kafka.NewConsumer(addrs, group, topic, failHandler, logger)
	require.NoError(t, err)

	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		consumer1.Run(ctx1)
	}()

	require.Eventually(t, func() bool { return firstCallCount.Load() >= 1 }, 10*time.Second, 50*time.Millisecond)
	cancel1()
	<-done1
	consumer1.Close()

	// Second consumer in the same group must receive the message again.
	var secondCallCount atomic.Int32
	successHandler := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		secondCallCount.Add(1)
		return nil
	})

	consumer2, err := kafka.NewConsumer(addrs, group, topic, successHandler, logger)
	require.NoError(t, err)

	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		consumer2.Run(ctx2)
	}()

	require.Eventually(t, func() bool { return secondCallCount.Load() >= 1 }, 10*time.Second, 100*time.Millisecond,
		"second consumer must receive the unacknowledged message")

	cancel2()
	<-done2
	consumer2.Close()
}

// TestConsumer_ContextCancelled verifies Run exits cleanly when the context is cancelled.
func TestConsumer_ContextCancelled(t *testing.T) {
	_, addrs := newFakeCluster(t, "test.cancel")
	logger := newTestLogger()

	handler := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error { return nil })

	consumer, err := kafka.NewConsumer(addrs, "cancel-group", "test.cancel", handler, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer.Run did not exit after context cancellation")
	}

	consumer.Close()
}

// TestWithIdempotency_SkipsDuplicate verifies the same message_id is processed only once.
func TestWithIdempotency_SkipsDuplicate(t *testing.T) {
	logger := newTestLogger()

	var callCount atomic.Int32
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	handler := kafka.Chain(base, kafka.WithIdempotency(logger, 100))

	msgID := uuid.New().String()
	record := &kgo.Record{
		Headers: []kgo.RecordHeader{{Key: "message_id", Value: []byte(msgID)}},
	}

	require.NoError(t, handler.Handle(context.Background(), record))
	require.NoError(t, handler.Handle(context.Background(), record))

	assert.EqualValues(t, 1, callCount.Load(), "duplicate message_id must be processed only once")
}

// TestWithIdempotency_NoMessageID verifies records without message_id are not deduped.
func TestWithIdempotency_NoMessageID(t *testing.T) {
	logger := newTestLogger()

	var callCount atomic.Int32
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	handler := kafka.Chain(base, kafka.WithIdempotency(logger, 100))

	record := &kgo.Record{} // no headers

	require.NoError(t, handler.Handle(context.Background(), record))
	require.NoError(t, handler.Handle(context.Background(), record))

	assert.EqualValues(t, 2, callCount.Load(), "records without message_id must not be deduped")
}

// TestWithIdempotency_EvictsOldest pins the cost of the bound: the dedup set is
// finite, so a message redelivered after `limit` newer messages have passed is
// seen as new again. That is the trade an in-memory store makes, and it must be
// visible in a test rather than discovered in production.
func TestWithIdempotency_EvictsOldest(t *testing.T) {
	logger := newTestLogger()

	var callCount atomic.Int32
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	const limit = 3
	handler := kafka.Chain(base, kafka.WithIdempotency(logger, limit))

	recordFor := func(id string) *kgo.Record {
		return &kgo.Record{Headers: []kgo.RecordHeader{{Key: "message_id", Value: []byte(id)}}}
	}

	// Fill past the bound: inserting "4" evicts "1".
	for _, id := range []string{"1", "2", "3", "4"} {
		require.NoError(t, handler.Handle(context.Background(), recordFor(id)))
	}
	assert.EqualValues(t, 4, callCount.Load(), "four distinct IDs must all be processed")

	// "1" was evicted, so it is treated as new.
	require.NoError(t, handler.Handle(context.Background(), recordFor("1")))
	assert.EqualValues(t, 5, callCount.Load(), "an evicted message_id must be treated as new")

	// "4" is still within the bound, so it is still recognised as a duplicate.
	require.NoError(t, handler.Handle(context.Background(), recordFor("4")))
	assert.EqualValues(t, 5, callCount.Load(), "a retained message_id must still be deduped")
}

// TestWithIdempotency_ConcurrentAdd guards the mutex that replaced the
// concurrent map. Dispatch is single-goroutine today, but the middleware is a
// composition point and nothing in its signature says otherwise.
func TestWithIdempotency_ConcurrentAdd(t *testing.T) {
	logger := newTestLogger()

	var callCount atomic.Int32
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	handler := kafka.Chain(base, kafka.WithIdempotency(logger, 1_000))

	const goroutines = 8
	const perGoroutine = 50

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				record := &kgo.Record{Headers: []kgo.RecordHeader{
					{Key: "message_id", Value: fmt.Appendf(nil, "g%d-%d", g, i)},
				}}
				// Handle each ID twice: the second must always be skipped.
				_ = handler.Handle(context.Background(), record)
				_ = handler.Handle(context.Background(), record)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, goroutines*perGoroutine, callCount.Load(),
		"each distinct message_id must reach the handler exactly once")
}

// TestWithIdempotency_NonPositiveLimit covers the misconfiguration path: a zero
// or negative bound must fall back to the default, never to an unbounded set.
func TestWithIdempotency_NonPositiveLimit(t *testing.T) {
	logger := newTestLogger()

	var callCount atomic.Int32
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		callCount.Add(1)
		return nil
	})

	handler := kafka.Chain(base, kafka.WithIdempotency(logger, 0))

	record := &kgo.Record{Headers: []kgo.RecordHeader{{Key: "message_id", Value: []byte("x")}}}
	require.NoError(t, handler.Handle(context.Background(), record))
	require.NoError(t, handler.Handle(context.Background(), record))

	assert.EqualValues(t, 1, callCount.Load(), "a non-positive limit must still dedup")
}

// TestWithRecovery_CatchesPanic verifies panics are converted to errors without crashing.
func TestWithRecovery_CatchesPanic(t *testing.T) {
	logger := newTestLogger()

	panicHandler := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		panic("something went very wrong")
	})

	handler := kafka.Chain(panicHandler, kafka.WithRecovery(logger))

	err := handler.Handle(context.Background(), &kgo.Record{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
}

// TestWithLogging_PassesThrough verifies the logging middleware does not alter success results.
func TestWithLogging_PassesThrough(t *testing.T) {
	logger := newTestLogger()

	var called bool
	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		called = true
		return nil
	})

	handler := kafka.Chain(base, kafka.WithLogging(logger))
	require.NoError(t, handler.Handle(context.Background(), &kgo.Record{}))
	assert.True(t, called)
}

// TestChain_MiddlewareOrder verifies middlewares wrap in declaration order (first = outermost).
func TestChain_MiddlewareOrder(t *testing.T) {
	var order []string

	makeMiddleware := func(name string) kafka.Middleware {
		return func(next kafka.Handler) kafka.Handler {
			return kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
				order = append(order, name+":before")
				err := next.Handle(ctx, record)
				order = append(order, name+":after")
				return err
			})
		}
	}

	base := kafka.HandlerFunc(func(ctx context.Context, record *kgo.Record) error {
		order = append(order, "handler")
		return nil
	})

	handler := kafka.Chain(base, makeMiddleware("A"), makeMiddleware("B"))
	require.NoError(t, handler.Handle(context.Background(), &kgo.Record{}))

	assert.Equal(t, []string{"A:before", "B:before", "handler", "B:after", "A:after"}, order)
}

// failEveryProduce makes every produce request to fk fail with a non-retriable
// error and counts the requests it saw. The error code is what makes the count
// meaningful: franz-go does not retry a non-retriable code, so the counter ends
// up holding exactly the number of times Publish asked a broker to store the
// record.
func failEveryProduce(t *testing.T, fk *kfake.Cluster, count *atomic.Int32) {
	t.Helper()

	fk.ControlKey(kmsg.Produce.Int16(), func(req kmsg.Request) (kmsg.Response, error, bool) {
		pr, ok := req.(*kmsg.ProduceRequest)
		if !ok {
			return nil, nil, false
		}

		resp := pr.ResponseKind().(*kmsg.ProduceResponse)
		for _, reqTopic := range pr.Topics {
			respTopic := kmsg.NewProduceResponseTopic()
			// Both identifiers are echoed back: produce v13 addresses topics by
			// ID, earlier versions by name.
			respTopic.Topic = reqTopic.Topic
			respTopic.TopicID = reqTopic.TopicID
			for _, part := range reqTopic.Partitions {
				respPart := kmsg.NewProduceResponseTopicPartition()
				respPart.Partition = part.Partition
				respPart.ErrorCode = kerr.RecordListTooLarge.Code
				respTopic.Partitions = append(respTopic.Partitions, respPart)
			}
			resp.Topics = append(resp.Topics, respTopic)
		}

		count.Add(1)
		fk.KeepControl()
		return resp, nil, true
	})
}

// TestProducer_SingleAttemptThenDLQ pins the retry topology: Publish asks the
// broker exactly once and hands the failure to the DLQ. The application retry
// loop this replaces would have made retries+1 attempts per Publish, on top of
// whatever the client did on its own.
//
// The DLQ lives on a second cluster so the failure injection above can fail
// every produce it sees without also breaking the DLQ write.
func TestProducer_SingleAttemptThenDLQ(t *testing.T) {
	const retries = 3
	topic := "test.events.single"
	dlqTopic := "test.events.single.dlq"
	fk, addrs := newFakeCluster(t, topic)
	_, dlqAddrs := newFakeCluster(t, dlqTopic)
	logger := newTestLogger()

	var produceRequests atomic.Int32
	failEveryProduce(t, fk, &produceRequests)

	dlqWriter, err := kafka.NewDLQWriter(dlqAddrs, dlqTopic, logger)
	require.NoError(t, err)
	defer dlqWriter.Close()

	producer, err := kafka.NewProducer(addrs, topic, dlqWriter, retries, logger)
	require.NoError(t, err)
	defer producer.Close()

	msg := newOutboxMsg("CustomerRegistered")
	// The DLQ write succeeds, so Publish reports success to the poller: the
	// message is accounted for, just not on the happy path.
	require.NoError(t, producer.Publish(context.Background(), msg))

	assert.Equal(t, int32(1), produceRequests.Load(),
		"Publish must make exactly one produce attempt and leave retries to the client")

	// The record must actually be in the DLQ, not merely counted as failed.
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(dlqAddrs...),
		kgo.ConsumeTopics(dlqTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err)
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fetches := cl.PollFetches(ctx)
	require.NoError(t, fetches.Err())

	records := fetches.Records()
	require.Len(t, records, 1)
	assert.Equal(t, msg.AggregateID.String(), string(records[0].Key))
}

// TestProducer_CancelledContext covers the other half of removing the loop: a
// cancelled context ends the publish instead of feeding a retry that cannot
// succeed.
func TestProducer_CancelledContext(t *testing.T) {
	topic := "test.events.cancelled"
	dlqTopic := "test.events.cancelled.dlq"
	_, addrs := newFakeCluster(t, topic, dlqTopic)
	logger := newTestLogger()

	dlqWriter, err := kafka.NewDLQWriter(addrs, dlqTopic, logger)
	require.NoError(t, err)
	defer dlqWriter.Close()

	producer, err := kafka.NewProducer(addrs, topic, dlqWriter, 3, logger)
	require.NoError(t, err)
	defer producer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- producer.Publish(ctx, newOutboxMsg("CustomerRegistered")) }()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Publish did not return on a cancelled context")
	}
}
