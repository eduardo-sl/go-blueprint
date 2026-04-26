package customer

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Save(ctx context.Context, c Customer) error
	Update(ctx context.Context, c Customer) error
	Delete(ctx context.Context, id uuid.UUID) error
	FindByID(ctx context.Context, id uuid.UUID) (Customer, error)
	FindByEmail(ctx context.Context, email string) (Customer, error)
	List(ctx context.Context) ([]Customer, error)
}
