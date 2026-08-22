package server_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/eduardo-sl/go-blueprint/internal/platform/config"
	"github.com/eduardo-sl/go-blueprint/internal/platform/server"
	"github.com/eduardo-sl/go-blueprint/internal/product"
	"github.com/eduardo-sl/go-blueprint/internal/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The graceful-drain regressions. Before the fix, Start called e.Shutdown on an
// Echo instance whose Server was never started, so shutdown returned instantly
// and the process exited underneath in-flight handlers.
//
// /health is the probe: it is registered by Start itself and calls
// CachePinger.Ping with the request context, so a slow pinger is enough to hold
// a request open across shutdown without wiring a working domain handler.

func TestStart_DrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	pinger := newSlowPinger(300 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, started := startServer(t, ctx, pinger)

	type result struct {
		status int
		err    error
		at     time.Time
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/health") //nolint:noctx // drain probe
		if err != nil {
			resCh <- result{err: err, at: time.Now()}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		resCh <- result{status: resp.StatusCode, at: time.Now()}
	}()

	// Cancel while the handler is still inside Ping.
	<-pinger.entered
	cancel()

	res := <-resCh
	start := <-started

	require.NoError(t, res.err, "in-flight request was cut off by shutdown")
	assert.Equal(t, http.StatusOK, res.status)
	require.NoError(t, start.err)
	assert.False(t, start.at.Before(res.at),
		"Start returned before the in-flight request completed")
}

func TestStart_ShutdownTimeoutExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("takes the full 10s shutdown timeout")
	}
	t.Parallel()

	// Longer than the server's 10s shutdown timeout, so the drain cannot finish.
	pinger := newSlowPinger(15 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, started := startServer(t, ctx, pinger)

	go func() {
		resp, err := http.Get("http://" + addr + "/health") //nolint:noctx // drain probe
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-pinger.entered
	cancel()

	select {
	case start := <-started:
		require.Error(t, start.err, "a drain that times out must surface an error")
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after the shutdown timeout elapsed")
	}
}

// --- harness ---

// startResult is what server.Start returned, and when.
type startResult struct {
	err error
	at  time.Time
}

// startServer runs server.Start on a free port and returns the address plus a
// channel that yields the Start outcome.
func startServer(t *testing.T, ctx context.Context, pinger server.CachePinger) (string, <-chan startResult) {
	t.Helper()

	cfg := &config.Config{
		Env:             "test",
		Addr:            freeAddr(t),
		JWTSecret:       "test-secret-at-least-32-characters-long",
		JWTExpiry:       time.Hour,
		OTelServiceName: "server-test",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	repo := &stubCustomerRepo{}
	customerSvc := customer.NewService(
		repo, &stubBeginner{}, &stubOutbox{}, &stubEventLog{}, cache.NoopCache{}, logger,
	)
	customerHandler := customer.NewHandler(customerSvc, customer.NewQueryService(repo))
	preferencesHandler := customer.NewPreferencesHandler(
		customer.NewPreferencesService(&stubPreferencesRepo{}),
	)
	authHandler := auth.NewHandler(
		auth.NewService(&stubAuthRepo{}, cfg.JWTSecret, cfg.JWTExpiry, logger),
	)
	productRepo := &stubProductRepo{}
	productHandler := product.NewHandler(
		product.NewService(productRepo, logger), product.NewQueryService(productRepo),
	)

	pool := worker.New(context.Background(), 1, 1, logger)
	t.Cleanup(func() { pool.Stop() })

	returned := make(chan startResult, 1)
	go func() {
		err := server.Start(ctx, cfg, customerHandler, preferencesHandler, authHandler,
			productHandler, pinger, pool, logger)
		returned <- startResult{err: err, at: time.Now()}
	}()

	waitForListener(t, cfg.Addr)
	return cfg.Addr, returned
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			require.NoError(t, conn.Close())
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}

// slowPinger holds the /health handler open for delay, signalling entered once
// the request is actually inside the handler.
type slowPinger struct {
	delay   time.Duration
	entered chan struct{}
	once    sync.Once
}

func newSlowPinger(delay time.Duration) *slowPinger {
	return &slowPinger{delay: delay, entered: make(chan struct{})}
}

func (p *slowPinger) Ping(ctx context.Context) error {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
	}
	return nil
}

// --- domain stubs: enough to construct handlers, never exercised ---

type stubCustomerRepo struct{}

func (stubCustomerRepo) SaveTx(context.Context, pgx.Tx, customer.Customer) error   { return nil }
func (stubCustomerRepo) UpdateTx(context.Context, pgx.Tx, customer.Customer) error { return nil }
func (stubCustomerRepo) DeleteTx(context.Context, pgx.Tx, uuid.UUID) error         { return nil }
func (stubCustomerRepo) FindByID(context.Context, uuid.UUID) (customer.Customer, error) {
	return customer.Customer{}, customer.ErrNotFound
}
func (stubCustomerRepo) FindByEmail(context.Context, string) (customer.Customer, error) {
	return customer.Customer{}, customer.ErrNotFound
}
func (stubCustomerRepo) List(context.Context) ([]customer.Customer, error) { return nil, nil }

type stubBeginner struct{}

func (stubBeginner) Begin(context.Context) (pgx.Tx, error) { return nil, context.Canceled }

type stubOutbox struct{}

func (stubOutbox) SaveTx(context.Context, pgx.Tx, outbox.OutboxMessage) error { return nil }
func (stubOutbox) ClaimBatch(context.Context, int, time.Duration, int) ([]outbox.OutboxMessage, error) {
	return nil, nil
}
func (stubOutbox) MarkProcessed(context.Context, uuid.UUID) error { return nil }
func (stubOutbox) MarkFailed(context.Context, uuid.UUID, string, time.Duration) error {
	return nil
}
func (stubOutbox) MarkExhausted(context.Context, uuid.UUID, string) error { return nil }

type stubEventLog struct{}

func (stubEventLog) Append(context.Context, eventlog.Event) error { return nil }
func (stubEventLog) FetchSince(context.Context, string, time.Time) ([]eventlog.Event, error) {
	return nil, nil
}

type stubPreferencesRepo struct{}

func (stubPreferencesRepo) Upsert(context.Context, customer.CustomerPreferences) error { return nil }
func (stubPreferencesRepo) FindByCustomerID(context.Context, uuid.UUID) (customer.CustomerPreferences, error) {
	return customer.CustomerPreferences{}, customer.ErrPreferencesNotFound
}

type stubAuthRepo struct{}

func (stubAuthRepo) Save(context.Context, auth.User) error { return nil }
func (stubAuthRepo) FindByEmail(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrUserNotFound
}
func (stubAuthRepo) FindByID(context.Context, uuid.UUID) (auth.User, error) {
	return auth.User{}, auth.ErrUserNotFound
}

type stubProductRepo struct{}

func (stubProductRepo) Save(context.Context, product.Product) error   { return nil }
func (stubProductRepo) Update(context.Context, product.Product) error { return nil }
func (stubProductRepo) FindByID(context.Context, bson.ObjectID) (product.Product, error) {
	return product.Product{}, product.ErrProductNotFound
}
func (stubProductRepo) FindBySKU(context.Context, string) (product.Product, error) {
	return product.Product{}, product.ErrProductNotFound
}
func (stubProductRepo) Search(context.Context, string, product.Category, int) ([]product.Product, error) {
	return nil, nil
}
func (stubProductRepo) FindByCategory(context.Context, product.Category, map[string]string, int, int) ([]product.Product, error) {
	return nil, nil
}
