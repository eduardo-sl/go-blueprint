package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CustomerRepository struct {
	q *Queries
}

func NewCustomerRepository(pool *pgxpool.Pool) *CustomerRepository {
	return &CustomerRepository{q: New(pool)}
}

func (r *CustomerRepository) Save(ctx context.Context, c customer.Customer) error {
	_, err := r.q.CreateCustomer(ctx, CreateCustomerParams{
		ID:        c.ID,
		Name:      c.Name,
		Email:     c.Email,
		BirthDate: toPgDate(c.BirthDate),
		CreatedAt: toPgTimestamptz(c.CreatedAt),
		UpdatedAt: toPgTimestamptz(c.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("postgres.CustomerRepository.Save: %w", err)
	}
	return nil
}

func (r *CustomerRepository) Update(ctx context.Context, c customer.Customer) error {
	_, err := r.q.UpdateCustomer(ctx, UpdateCustomerParams{
		ID:        c.ID,
		Name:      c.Name,
		Email:     c.Email,
		BirthDate: toPgDate(c.BirthDate),
		UpdatedAt: toPgTimestamptz(c.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("postgres.CustomerRepository.Update: %w", err)
	}
	return nil
}

func (r *CustomerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.q.DeleteCustomer(ctx, id); err != nil {
		return fmt.Errorf("postgres.CustomerRepository.Delete: %w", err)
	}
	return nil
}

func (r *CustomerRepository) FindByID(ctx context.Context, id uuid.UUID) (customer.Customer, error) {
	row, err := r.q.GetCustomerByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return customer.Customer{}, customer.ErrNotFound
		}
		return customer.Customer{}, fmt.Errorf("postgres.CustomerRepository.FindByID: %w", err)
	}
	return fromRow(row), nil
}

func (r *CustomerRepository) FindByEmail(ctx context.Context, email string) (customer.Customer, error) {
	row, err := r.q.GetCustomerByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return customer.Customer{}, customer.ErrNotFound
		}
		return customer.Customer{}, fmt.Errorf("postgres.CustomerRepository.FindByEmail: %w", err)
	}
	return fromRow(row), nil
}

func (r *CustomerRepository) List(ctx context.Context) ([]customer.Customer, error) {
	rows, err := r.q.ListCustomers(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres.CustomerRepository.List: %w", err)
	}
	result := make([]customer.Customer, len(rows))
	for i, row := range rows {
		result[i] = fromRow(row)
	}
	return result, nil
}

func fromRow(row Customer) customer.Customer {
	return customer.Customer{
		ID:        row.ID,
		Name:      row.Name,
		Email:     row.Email,
		BirthDate: fromPgDate(row.BirthDate),
		CreatedAt: fromPgTimestamptz(row.CreatedAt),
		UpdatedAt: fromPgTimestamptz(row.UpdatedAt),
	}
}

func toPgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func fromPgTimestamptz(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func toPgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t.UTC(), Valid: true}
}

func fromPgDate(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time.UTC()
}
