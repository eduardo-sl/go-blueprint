package customer

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound         = errors.New("customer not found")
	ErrEmailExists      = errors.New("email already registered")
	ErrInvalidBirthDate = errors.New("birth date cannot be in the future")
	ErrNameRequired     = errors.New("name is required")
	ErrEmailRequired    = errors.New("email is required")
)

type Customer struct {
	ID        uuid.UUID
	Name      string
	Email     string
	BirthDate time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

func New(name, email string, birthDate time.Time) (Customer, error) {
	if err := validateName(name); err != nil {
		return Customer{}, err
	}
	if err := validateEmail(email); err != nil {
		return Customer{}, err
	}
	if err := validateBirthDate(birthDate); err != nil {
		return Customer{}, err
	}

	now := time.Now().UTC()
	return Customer{
		ID:        uuid.New(),
		Name:      strings.TrimSpace(name),
		Email:     strings.ToLower(strings.TrimSpace(email)),
		BirthDate: birthDate.UTC(),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (c *Customer) Update(name, email string, birthDate time.Time) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateEmail(email); err != nil {
		return err
	}
	if err := validateBirthDate(birthDate); err != nil {
		return err
	}

	c.Name = strings.TrimSpace(name)
	c.Email = strings.ToLower(strings.TrimSpace(email))
	c.BirthDate = birthDate.UTC()
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("customer.validateName: %w", ErrNameRequired)
	}
	return nil
}

func validateEmail(email string) error {
	if strings.TrimSpace(email) == "" {
		return fmt.Errorf("customer.validateEmail: %w", ErrEmailRequired)
	}
	return nil
}

func validateBirthDate(birthDate time.Time) error {
	if birthDate.After(time.Now()) {
		return fmt.Errorf("customer.validateBirthDate: %w", ErrInvalidBirthDate)
	}
	return nil
}
