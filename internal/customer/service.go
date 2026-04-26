package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/eventlog"
	"github.com/google/uuid"
)

type Service struct {
	repo     Repository
	eventLog eventlog.Store
	logger   *slog.Logger
}

func NewService(repo Repository, el eventlog.Store, logger *slog.Logger) *Service {
	return &Service{repo: repo, eventLog: el, logger: logger}
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

	if err := s.repo.Save(ctx, c); err != nil {
		return uuid.Nil, fmt.Errorf("customer.Service.Register: save: %w", err)
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

	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("customer.Service.Update: save: %w", err)
	}

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

	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("customer.Service.Remove: delete: %w", err)
	}

	s.appendEvent(ctx, "CustomerRemoved", id, map[string]any{"id": id.String()})

	return nil
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
