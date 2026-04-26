package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRepository struct {
	q *Queries
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{q: New(pool)}
}

func (r *UserRepository) Save(ctx context.Context, u auth.User) error {
	_, err := r.q.CreateUser(ctx, CreateUserParams{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Name:         u.Name,
		CreatedAt:    toPgTimestamptz(u.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("postgres.UserRepository.Save: %w", err)
	}
	return nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (auth.User, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.User{}, auth.ErrUserNotFound
		}
		return auth.User{}, fmt.Errorf("postgres.UserRepository.FindByEmail: %w", err)
	}
	return fromUserRow(row), nil
}

func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (auth.User, error) {
	row, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.User{}, auth.ErrUserNotFound
		}
		return auth.User{}, fmt.Errorf("postgres.UserRepository.FindByID: %w", err)
	}
	return fromUserRow(row), nil
}

func fromUserRow(row User) auth.User {
	var createdAt time.Time
	if row.CreatedAt.Valid {
		createdAt = row.CreatedAt.Time.UTC()
	}
	return auth.User{
		ID:           row.ID,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		Name:         row.Name,
		CreatedAt:    createdAt,
	}
}
