package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUserNotFound     = errors.New("user not found")
	ErrEmailExists      = errors.New("email already registered")
	ErrInvalidPassword  = errors.New("invalid password")
	ErrEmailRequired    = errors.New("email is required")
	ErrNameRequired     = errors.New("name is required")
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
)

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
}

func NewUser(email, name, passwordHash string) (User, error) {
	if strings.TrimSpace(email) == "" {
		return User{}, ErrEmailRequired
	}
	if strings.TrimSpace(name) == "" {
		return User{}, ErrNameRequired
	}

	return User{
		ID:           uuid.New(),
		Email:        strings.ToLower(strings.TrimSpace(email)),
		Name:         strings.TrimSpace(name),
		PasswordHash: passwordHash,
		CreatedAt:    time.Now().UTC(),
	}, nil
}
