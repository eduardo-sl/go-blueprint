package customer

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// CustomerPreferences holds a customer's product preferences.
// Stored in MongoDB — not in Postgres — because the structure is semi-structured
// and changes independently of the Customer entity.
type CustomerPreferences struct {
	CustomerID         uuid.UUID `bson:"customer_id"` // FK to Postgres Customer
	FavoriteCategories []string  `bson:"favorite_categories"`
	WatchList          []string  `bson:"watchlist"` // product IDs (MongoDB ObjectID hex strings)
	UpdatedAt          time.Time `bson:"updated_at"`
}

// PreferencesRepository is defined at the consumer (customer package).
// The MongoDB implementation in platform/database/mongodb satisfies it implicitly.
type PreferencesRepository interface {
	Upsert(ctx context.Context, prefs CustomerPreferences) error
	FindByCustomerID(ctx context.Context, customerID uuid.UUID) (CustomerPreferences, error)
}

var ErrPreferencesNotFound = errors.New("customer preferences not found")
