package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	_bcryptCost     = 12
	_minPasswordLen = 8
)

type Service struct {
	repo      Repository
	jwtSecret []byte
	jwtExpiry time.Duration
	logger    *slog.Logger
}

func NewService(repo Repository, jwtSecret string, jwtExpiry time.Duration, logger *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		jwtSecret: []byte(jwtSecret),
		jwtExpiry: jwtExpiry,
		logger:    logger,
	}
}

type RegisterCmd struct {
	Email    string
	Name     string
	Password string
}

type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) Register(ctx context.Context, cmd RegisterCmd) (uuid.UUID, error) {
	if len(cmd.Password) < _minPasswordLen {
		return uuid.Nil, ErrPasswordTooShort
	}

	_, err := s.repo.FindByEmail(ctx, cmd.Email)
	if err == nil {
		return uuid.Nil, ErrEmailExists
	}
	if !errors.Is(err, ErrUserNotFound) {
		return uuid.Nil, fmt.Errorf("auth.Service.Register: check email: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(cmd.Password), _bcryptCost)
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth.Service.Register: hash password: %w", err)
	}

	u, err := NewUser(cmd.Email, cmd.Name, string(hash))
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth.Service.Register: new user: %w", err)
	}

	if err := s.repo.Save(ctx, u); err != nil {
		return uuid.Nil, fmt.Errorf("auth.Service.Register: save: %w", err)
	}

	return u.ID, nil
}

type LoginCmd struct {
	Email    string
	Password string
}

func (s *Service) Login(ctx context.Context, cmd LoginCmd) (TokenResponse, error) {
	u, err := s.repo.FindByEmail(ctx, cmd.Email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return TokenResponse{}, ErrInvalidPassword
		}
		return TokenResponse{}, fmt.Errorf("auth.Service.Login: find user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(cmd.Password)); err != nil {
		return TokenResponse{}, ErrInvalidPassword
	}

	expiresAt := time.Now().Add(s.jwtExpiry)
	claims := jwt.MapClaims{
		"sub":   u.ID.String(),
		"email": u.Email,
		"exp":   expiresAt.Unix(),
		"iat":   time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("auth.Service.Login: sign token: %w", err)
	}

	return TokenResponse{Token: signed, ExpiresAt: expiresAt}, nil
}
