package customer

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type QueryService struct {
	repo Repository
}

func NewQueryService(repo Repository) *QueryService {
	return &QueryService{repo: repo}
}

func (q *QueryService) GetByID(ctx context.Context, id uuid.UUID) (Customer, error) {
	c, err := q.repo.FindByID(ctx, id)
	if err != nil {
		return Customer{}, fmt.Errorf("customer.QueryService.GetByID: %w", err)
	}
	return c, nil
}

func (q *QueryService) List(ctx context.Context) ([]Customer, error) {
	cs, err := q.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("customer.QueryService.List: %w", err)
	}
	return cs, nil
}
