package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/eduardo-sl/go-blueprint/internal/outbox"
	"github.com/eduardo-sl/go-blueprint/internal/platform/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Beginner is satisfied by *pgxpool.Pool and any test stub.
// Defined here so the service does not import pgxpool directly.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Service struct {
	repo        Repository
	db          Beginner
	outboxStore outbox.OutboxStore
	eventLog    eventlog.Store
	cache       cache.Cache
	logger      *slog.Logger
}

func NewService(
	repo Repository,
	db Beginner,
	outboxStore outbox.OutboxStore,
	el eventlog.Store,
	c cache.Cache,
	logger *slog.Logger,
) *Service {
	return &Service{
		repo:        repo,
		db:          db,
		outboxStore: outboxStore,
		eventLog:    el,
		cache:       c,
		logger:      logger,
	}
}

type RegisterCmd struct {
	Name      string
	Email     string
	BirthDate time.Time
}

func (s *Service) Register(ctx context.Context, cmd RegisterCmd) (uuid.UUID, error) {
	_, err := s.repo.FindByEmail(ctx, cmd.Email)
	if err == nil {
		return uuid.Nil, ErrEmailExists
	}
	if !errors.Is(err, ErrNotFound) {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: check email: %w", err)
	}

	c, err := New(cmd.Name, cmd.Email, cmd.BirthDate)
	if err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: %w", err)
	}

	// Both the customer write and the outbox message must commit atomically.
	// If the process crashes after the customer is persisted but before the
	// outbox message is written, the event would be lost forever.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	if err := s.repo.SaveTx(ctx, tx, c); err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: save: %w", err)
	}

	if err := s.appendOutbox(ctx, tx, "CustomerRegistered", c.ID, map[string]any{
		"id":    c.ID.String(),
		"email": c.Email,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: commit: %w", err)
	}

	s.appendEvent(ctx, "CustomerRegistered", c.ID, map[string]any{
		"name":  c.Name,
		"email": c.Email,
	})

	return c.ID, nil
}

type UpdateCmd struct {
	ID        uuid.UUID
	Name      string
	Email     string
	BirthDate time.Time
}

func (s *Service) Update(ctx context.Context, cmd UpdateCmd) error {
	c, err := s.repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return fmt.Errorf("customer.Service.Update: find: %w", err)
	}

	if c.Email != cmd.Email {
		_, err := s.repo.FindByEmail(ctx, cmd.Email)
		if err == nil {
			return ErrEmailExists
		}
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("customer.Service.Update: check email: %w", err)
		}
	}

	if err := c.Update(cmd.Name, cmd.Email, cmd.BirthDate); err != nil {
		return fmt.Errorf("customer.Service.Update: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("customer.Service.Update: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("customer.Service.Update: save: %w", err)
	}

	if err := s.appendOutbox(ctx, tx, "CustomerUpdated", c.ID, map[string]any{
		"id":    c.ID.String(),
		"email": c.Email,
	}); err != nil {
		return fmt.Errorf("customer.Service.Update: outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("customer.Service.Update: commit: %w", err)
	}

	s.invalidate(ctx, c.ID)
	s.appendEvent(ctx, "CustomerUpdated", c.ID, map[string]any{
		"name":  c.Name,
		"email": c.Email,
	})

	return nil
}

func (s *Service) Remove(ctx context.Context, id uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return fmt.Errorf("customer.Service.Remove: find: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("customer.Service.Remove: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("customer.Service.Remove: delete: %w", err)
	}

	if err := s.appendOutbox(ctx, tx, "CustomerRemoved", id, map[string]any{
		"id": id.String(),
	}); err != nil {
		return fmt.Errorf("customer.Service.Remove: outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("customer.Service.Remove: commit: %w", err)
	}

	s.invalidate(ctx, id)
	s.appendEvent(ctx, "CustomerRemoved", id, map[string]any{"id": id.String()})

	return nil
}

// appendOutbox serialises payload and writes an OutboxMessage inside tx.
func (s *Service) appendOutbox(ctx context.Context, tx pgx.Tx, eventType string, aggregateID uuid.UUID, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	msg := outbox.OutboxMessage{
		ID:          uuid.New(),
		AggregateID: aggregateID,
		EventType:   eventType,
		Payload:     data,
		CreatedAt:   time.Now().UTC(),
	}
	return s.outboxStore.SaveTx(ctx, tx, msg)
}

// invalidate deletes the single-record key and the list key from cache.
// Failures are logged but never propagated — stale cache beats a failed write.
func (s *Service) invalidate(ctx context.Context, id uuid.UUID) {
	keys := []string{cacheKeyPrefix + id.String(), cacheKeyList}
	for _, key := range keys {
		if err := s.cache.Delete(ctx, key); err != nil {
			s.logger.WarnContext(ctx, "cache invalidation failed",
				slog.String("key", key), slog.Any("error", err))
		}
	}
}

func (s *Service) appendEvent(ctx context.Context, eventType string, aggregateID uuid.UUID, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		s.logger.ErrorContext(ctx, "customer: marshal event payload", slog.Any("error", err))
		return
	}

	e := eventlog.Event{
		ID:          uuid.New(),
		AggregateID: aggregateID,
		EventType:   eventType,
		Payload:     data,
		OccurredAt:  time.Now().UTC(),
	}

	if err := s.eventLog.Append(ctx, e); err != nil {
		s.logger.ErrorContext(ctx, "customer: append event", slog.Any("error", err))
	}
}
