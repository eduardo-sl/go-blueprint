package customer

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository is the persistence port for the customer aggregate.
//
// Every write is transaction-scoped. The caller owns Begin/Commit/Rollback and
// passes the same tx to the outbox store, so the aggregate row and its outbox
// message commit together or not at all. A non-transactional write method would
// be a silent way to break that guarantee, so none exists.
type Repository interface {
	SaveTx(ctx context.Context, tx pgx.Tx, c Customer) error
	UpdateTx(ctx context.Context, tx pgx.Tx, c Customer) error
	DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error

	FindByID(ctx context.Context, id uuid.UUID) (Customer, error)
	FindByEmail(ctx context.Context, email string) (Customer, error)
	List(ctx context.Context) ([]Customer, error)
}
