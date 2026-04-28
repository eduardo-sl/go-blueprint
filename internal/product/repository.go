package product

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Repository is the write-side persistence interface for the product aggregate.
// Defined at the consumer (product package) — the MongoDB implementation in
// platform/database/mongodb satisfies it implicitly.
type Repository interface {
	Save(ctx context.Context, p Product) error
	FindByID(ctx context.Context, id bson.ObjectID) (Product, error)
	FindBySKU(ctx context.Context, sku string) (Product, error)
	Update(ctx context.Context, p Product) error
	Search(ctx context.Context, query string, category Category, limit int) ([]Product, error)
	FindByCategory(ctx context.Context, category Category, attributeFilters map[string]string, limit, offset int) ([]Product, error)
}
