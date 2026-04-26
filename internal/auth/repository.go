package auth

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Save(ctx context.Context, u User) error
	FindByEmail(ctx context.Context, email string) (User, error)
	FindByID(ctx context.Context, id uuid.UUID) (User, error)
}
