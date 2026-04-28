package product

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type QueryService struct {
	repo Repository
}

func NewQueryService(repo Repository) *QueryService {
	return &QueryService{repo: repo}
}

func (q *QueryService) GetByID(ctx context.Context, id bson.ObjectID) (Product, error) {
	p, err := q.repo.FindByID(ctx, id)
	if err != nil {
		return Product{}, fmt.Errorf("product.QueryService.GetByID: %w", err)
	}
	return p, nil
}

// Search performs full-text search on name + description.
// Returns up to limit products ordered by relevance score.
func (q *QueryService) Search(ctx context.Context, query string, category Category, limit int) ([]Product, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	products, err := q.repo.Search(ctx, query, category, limit)
	if err != nil {
		return nil, fmt.Errorf("product.QueryService.Search: %w", err)
	}
	return products, nil
}

// ListByCategory returns active products filtered by category with optional attribute filters.
func (q *QueryService) ListByCategory(ctx context.Context, category Category, attributeFilters map[string]string, limit, offset int) ([]Product, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	products, err := q.repo.FindByCategory(ctx, category, attributeFilters, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("product.QueryService.ListByCategory: %w", err)
	}
	return products, nil
}
