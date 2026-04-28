package product

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Service struct {
	repo   Repository
	logger *slog.Logger
}

func NewService(repo Repository, logger *slog.Logger) *Service {
	return &Service{repo: repo, logger: logger}
}

type CreateCmd struct {
	Name        string
	Description string
	SKU         string
	Category    Category
	Attributes  map[string]any
	Variants    []Variant
	PriceCents  int64
}

func (s *Service) Create(ctx context.Context, cmd CreateCmd) (bson.ObjectID, error) {
	p, err := NewProduct(cmd.Name, cmd.Description, cmd.SKU, cmd.Category, cmd.Attributes, cmd.Variants, cmd.PriceCents)
	if err != nil {
		return bson.NilObjectID, fmt.Errorf("product.Service.Create: %w", err)
	}

	if err := s.repo.Save(ctx, p); err != nil {
		return bson.NilObjectID, fmt.Errorf("product.Service.Create: %w", err)
	}

	return p.ID, nil
}

type UpdateCmd struct {
	ID          bson.ObjectID
	Name        string
	Description string
	Attributes  map[string]any
	PriceCents  int64
}

func (s *Service) Update(ctx context.Context, cmd UpdateCmd) error {
	p, err := s.repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return fmt.Errorf("product.Service.Update: find: %w", err)
	}

	if err := p.UpdateDetails(cmd.Name, cmd.Description, cmd.Attributes, cmd.PriceCents); err != nil {
		return fmt.Errorf("product.Service.Update: %w", err)
	}

	if err := s.repo.Update(ctx, p); err != nil {
		return fmt.Errorf("product.Service.Update: save: %w", err)
	}

	return nil
}

func (s *Service) Archive(ctx context.Context, id bson.ObjectID) error {
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("product.Service.Archive: find: %w", err)
	}

	p.Archive()

	if err := s.repo.Update(ctx, p); err != nil {
		return fmt.Errorf("product.Service.Archive: save: %w", err)
	}

	return nil
}
