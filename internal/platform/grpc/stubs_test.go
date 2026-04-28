package grpc_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// noopRepo satisfies customer.Repository for tests that do not need real persistence.
type noopRepo struct{}

func (r *noopRepo) Save(_ context.Context, _ customer.Customer) error { return nil }
func (r *noopRepo) SaveTx(_ context.Context, _ pgx.Tx, _ customer.Customer) error { return nil }
func (r *noopRepo) Update(_ context.Context, _ customer.Customer) error { return nil }
func (r *noopRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (r *noopRepo) FindByID(_ context.Context, _ uuid.UUID) (customer.Customer, error) {
	return customer.Customer{}, customer.ErrNotFound
}
func (r *noopRepo) FindByEmail(_ context.Context, _ string) (customer.Customer, error) {
	return customer.Customer{}, customer.ErrNotFound
}
func (r *noopRepo) List(_ context.Context) ([]customer.Customer, error) { return nil, nil }

// noopBeginner satisfies customer.Beginner (pgx.Tx-returning Begin).
type noopBeginner struct{}

func (b *noopBeginner) Begin(_ context.Context) (pgx.Tx, error) { return &noopTx{}, nil }

type noopTx struct{ pgx.Tx }

func (t *noopTx) Commit(_ context.Context) error   { return nil }
func (t *noopTx) Rollback(_ context.Context) error { return nil }

// noopEventStore satisfies eventlog.Store.
type noopEventStore struct{}

func (n *noopEventStore) Append(_ context.Context, _ eventlog.Event) error { return nil }
func (n *noopEventStore) FetchSince(_ context.Context, _ string, _ time.Time) ([]eventlog.Event, error) {
	return nil, nil
}

// noopOutboxStore satisfies outbox.OutboxStore.
type noopOutboxStore struct{}

func (n *noopOutboxStore) SaveTx(_ context.Context, _ pgx.Tx, _ outbox.OutboxMessage) error {
	return nil
}
func (n *noopOutboxStore) FetchUnprocessed(_ context.Context, _ int) ([]outbox.OutboxMessage, error) {
	return nil, nil
}
func (n *noopOutboxStore) MarkProcessed(_ context.Context, _ uuid.UUID) error { return nil }
func (n *noopOutboxStore) MarkFailed(_ context.Context, _ uuid.UUID, _ string) error { return nil }

// noopAuthRepo satisfies auth.Repository.
type noopAuthRepo struct {
	users map[string]auth.User
}

func (r *noopAuthRepo) init() {
	if r.users == nil {
		r.users = make(map[string]auth.User)
	}
}

func (r *noopAuthRepo) Save(_ context.Context, u auth.User) error {
	r.init()
	r.users[u.Email] = u
	return nil
}

func (r *noopAuthRepo) FindByEmail(_ context.Context, email string) (auth.User, error) {
	r.init()
	u, ok := r.users[email]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

func (r *noopAuthRepo) FindByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	r.init()
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrUserNotFound
}

// ensure json is used (imported for eventlog.Event.Payload usage)
var _ = json.Marshal
