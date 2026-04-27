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
	"github.com/eduardo-sl/go-blueprint/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var svcTracer = otel.Tracer("customer.service")

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
	ctx, span := svcTracer.Start(ctx, "customer.Service.Register")
	defer span.End()

	span.SetAttributes(attribute.String("customer.email", cmd.Email))

	_, err := s.repo.FindByEmail(ctx, cmd.Email)
	if err == nil {
		span.SetStatus(codes.Error, ErrEmailExists.Error())
		return uuid.Nil, ErrEmailExists
	}
	if !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("customer.Service.Register: check email: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}

	c, err := New(cmd.Name, cmd.Email, cmd.BirthDate)
	if err != nil {
		err = fmt.Errorf("customer.Service.Register: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}

	// Both the customer write and the outbox message must commit atomically.
	// If the process crashes after the customer is persisted but before the
	// outbox message is written, the event would be lost forever.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		err = fmt.Errorf("customer.Service.Register: begin tx: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	if err := s.repo.SaveTx(ctx, tx, c); err != nil {
		err = fmt.Errorf("customer.Service.Register: save: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}

	if err := s.appendOutbox(ctx, tx, "CustomerRegistered", c.ID, map[string]any{
		"id":    c.ID.String(),
		"email": c.Email,
	}); err != nil {
		err = fmt.Errorf("customer.Service.Register: outbox: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		err = fmt.Errorf("customer.Service.Register: commit: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, err
	}

	span.SetAttributes(attribute.String("customer.id", c.ID.String()))
	telemetry.CustomerRegistrations.Add(ctx, 1)

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
	ctx, span := svcTracer.Start(ctx, "customer.Service.Update")
	defer span.End()

	span.SetAttributes(attribute.String("customer.id", cmd.ID.String()))

	c, err := s.repo.FindByID(ctx, cmd.ID)
	if err != nil {
		err = fmt.Errorf("customer.Service.Update: find: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if c.Email != cmd.Email {
		_, err := s.repo.FindByEmail(ctx, cmd.Email)
		if err == nil {
			span.SetStatus(codes.Error, ErrEmailExists.Error())
			return ErrEmailExists
		}
		if !errors.Is(err, ErrNotFound) {
			err = fmt.Errorf("customer.Service.Update: check email: %w", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	if err := c.Update(cmd.Name, cmd.Email, cmd.BirthDate); err != nil {
		err = fmt.Errorf("customer.Service.Update: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		err = fmt.Errorf("customer.Service.Update: begin tx: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.Update(ctx, c); err != nil {
		err = fmt.Errorf("customer.Service.Update: save: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := s.appendOutbox(ctx, tx, "CustomerUpdated", c.ID, map[string]any{
		"id":    c.ID.String(),
		"email": c.Email,
	}); err != nil {
		err = fmt.Errorf("customer.Service.Update: outbox: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		err = fmt.Errorf("customer.Service.Update: commit: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	s.invalidate(ctx, c.ID)
	s.appendEvent(ctx, "CustomerUpdated", c.ID, map[string]any{
		"name":  c.Name,
		"email": c.Email,
	})

	return nil
}

func (s *Service) Remove(ctx context.Context, id uuid.UUID) error {
	ctx, span := svcTracer.Start(ctx, "customer.Service.Remove")
	defer span.End()

	span.SetAttributes(attribute.String("customer.id", id.String()))

	if _, err := s.repo.FindByID(ctx, id); err != nil {
		err = fmt.Errorf("customer.Service.Remove: find: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		err = fmt.Errorf("customer.Service.Remove: begin tx: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.Delete(ctx, id); err != nil {
		err = fmt.Errorf("customer.Service.Remove: delete: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := s.appendOutbox(ctx, tx, "CustomerRemoved", id, map[string]any{
		"id": id.String(),
	}); err != nil {
		err = fmt.Errorf("customer.Service.Remove: outbox: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		err = fmt.Errorf("customer.Service.Remove: commit: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	telemetry.CustomerRemovals.Add(ctx, 1)
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
