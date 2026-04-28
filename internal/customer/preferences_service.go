package customer

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PreferencesService struct {
	repo PreferencesRepository
}

func NewPreferencesService(repo PreferencesRepository) *PreferencesService {
	return &PreferencesService{repo: repo}
}

type UpsertPreferencesCmd struct {
	CustomerID         uuid.UUID
	FavoriteCategories []string
	WatchList          []string
}

func (s *PreferencesService) Upsert(ctx context.Context, cmd UpsertPreferencesCmd) error {
	prefs := CustomerPreferences{
		CustomerID:         cmd.CustomerID,
		FavoriteCategories: cmd.FavoriteCategories,
		WatchList:          cmd.WatchList,
		UpdatedAt:          time.Now().UTC(),
	}
	if err := s.repo.Upsert(ctx, prefs); err != nil {
		return fmt.Errorf("customer.PreferencesService.Upsert: %w", err)
	}
	return nil
}

func (s *PreferencesService) GetByCustomerID(ctx context.Context, customerID uuid.UUID) (CustomerPreferences, error) {
	prefs, err := s.repo.FindByCustomerID(ctx, customerID)
	if err != nil {
		return CustomerPreferences{}, fmt.Errorf("customer.PreferencesService.GetByCustomerID: %w", err)
	}
	return prefs, nil
}
