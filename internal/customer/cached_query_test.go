package customer_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock cache ---

type mockCache struct {
	store   map[string][]byte
	getErr  error
	setErr  error
	deleted []string
}

func newMockCache() *mockCache {
	return &mockCache{store: make(map[string][]byte)}
}

func (m *mockCache) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.store[key]
	if !ok {
		return nil, cache.ErrCacheMiss
	}
	return v, nil
}

func (m *mockCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.store[key] = value
	return nil
}

func (m *mockCache) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	m.deleted = append(m.deleted, key)
	return nil
}

func (m *mockCache) Ping(_ context.Context) error { return nil }

// --- counting repo wrapper ---

type countingRepo struct {
	inner    *mockRepo
	getCalls int
}

func (r *countingRepo) Save(ctx context.Context, c customer.Customer) error {
	return r.inner.Save(ctx, c)
}
func (r *countingRepo) Update(ctx context.Context, c customer.Customer) error {
	return r.inner.Update(ctx, c)
}
func (r *countingRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return r.inner.Delete(ctx, id)
}
func (r *countingRepo) FindByEmail(ctx context.Context, email string) (customer.Customer, error) {
	return r.inner.FindByEmail(ctx, email)
}
func (r *countingRepo) List(ctx context.Context) ([]customer.Customer, error) {
	r.getCalls++
	return r.inner.List(ctx)
}
func (r *countingRepo) FindByID(ctx context.Context, id uuid.UUID) (customer.Customer, error) {
	r.getCalls++
	return r.inner.FindByID(ctx, id)
}

func newCachedQuery(repo customer.Repository, c cache.Cache) *customer.CachedQueryService {
	return customer.NewCachedQueryService(
		customer.NewQueryService(repo),
		c,
		5*time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func seedCustomer(t *testing.T, repo *mockRepo) customer.Customer {
	t.Helper()
	c, err := customer.New("Alice", "alice@example.com", time.Now().AddDate(-20, 0, 0))
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), c))
	return c
}

func TestCachedQueryService_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupCache  func(*mockCache, customer.Customer)
		cacheGetErr error
		cacheSetErr error
		wantDBCalls int
		wantErr     bool
		checkCached bool
	}{
		{
			name: "cache hit — no DB call",
			setupCache: func(mc *mockCache, c customer.Customer) {
				data, _ := json.Marshal(c)
				mc.store["customer:"+c.ID.String()] = data
			},
			wantDBCalls: 0,
		},
		{
			name:        "cache miss — calls DB and populates cache",
			wantDBCalls: 1,
			checkCached: true,
		},
		{
			name: "corrupted cache entry — evicts and calls DB",
			setupCache: func(mc *mockCache, c customer.Customer) {
				mc.store["customer:"+c.ID.String()] = []byte("not-json{{{{")
			},
			wantDBCalls: 1,
			checkCached: true,
		},
		{
			name:        "redis Get error — degrades to DB",
			cacheGetErr: errors.New("connection refused"),
			wantDBCalls: 1,
		},
		{
			name:        "redis Set error — returns result without error",
			cacheSetErr: errors.New("connection refused"),
			wantDBCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockRepo()
			c := seedCustomer(t, repo)

			mc := newMockCache()
			mc.getErr = tt.cacheGetErr
			mc.setErr = tt.cacheSetErr
			if tt.setupCache != nil {
				tt.setupCache(mc, c)
			}

			cr := &countingRepo{inner: repo}
			svc := newCachedQuery(cr, mc)

			got, err := svc.GetByID(context.Background(), c.ID)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, c.ID, got.ID)
			assert.Equal(t, tt.wantDBCalls, cr.getCalls)

			if tt.checkCached {
				key := "customer:" + c.ID.String()
				_, inCache := mc.store[key]
				assert.True(t, inCache, "expected value to be cached after DB hit")
			}
		})
	}
}

func TestCachedQueryService_GetByID_DBError(t *testing.T) {
	t.Parallel()

	repo := newMockRepo()
	mc := newMockCache()
	svc := newCachedQuery(repo, mc)

	_, err := svc.GetByID(context.Background(), uuid.New())

	require.Error(t, err)
	assert.True(t, errors.Is(err, customer.ErrNotFound))
}

func TestCachedQueryService_List(t *testing.T) {
	t.Parallel()

	t.Run("cache miss then hit", func(t *testing.T) {
		t.Parallel()

		repo := newMockRepo()
		seedCustomer(t, repo)

		mc := newMockCache()
		cr := &countingRepo{inner: repo}
		svc := newCachedQuery(cr, mc)
		ctx := context.Background()

		first, err := svc.List(ctx)
		require.NoError(t, err)
		assert.Len(t, first, 1)
		assert.Equal(t, 1, cr.getCalls)

		second, err := svc.List(ctx)
		require.NoError(t, err)
		assert.Len(t, second, 1)
		assert.Equal(t, 1, cr.getCalls, "second call should hit cache")
	})

	t.Run("corrupted list cache — evicts and calls DB", func(t *testing.T) {
		t.Parallel()

		repo := newMockRepo()
		seedCustomer(t, repo)

		mc := newMockCache()
		mc.store["customer:list"] = []byte("bad-json{{")

		cr := &countingRepo{inner: repo}
		svc := newCachedQuery(cr, mc)

		got, err := svc.List(context.Background())
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Equal(t, 1, cr.getCalls)
		assert.Contains(t, mc.deleted, "customer:list")
	})
}
