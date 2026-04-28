package mongodb

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduardo-sl/go-blueprint/internal/customer"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// PreferencesRepository implements customer.PreferencesRepository using MongoDB.
type PreferencesRepository struct {
	col *mongo.Collection
}

func NewPreferencesRepository(db *mongo.Database) *PreferencesRepository {
	return &PreferencesRepository{col: db.Collection("customer_preferences")}
}

func (r *PreferencesRepository) Upsert(ctx context.Context, prefs customer.CustomerPreferences) error {
	filter := bson.M{"customer_id": prefs.CustomerID.String()}
	update := bson.M{"$set": prefs}
	opts := options.UpdateOne().SetUpsert(true)

	_, err := r.col.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return fmt.Errorf("mongodb.PreferencesRepository.Upsert: %w", err)
	}
	return nil
}

func (r *PreferencesRepository) FindByCustomerID(ctx context.Context, customerID uuid.UUID) (customer.CustomerPreferences, error) {
	var prefs customer.CustomerPreferences
	err := r.col.FindOne(ctx, bson.M{"customer_id": customerID.String()}).Decode(&prefs)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return customer.CustomerPreferences{}, customer.ErrPreferencesNotFound
	}
	if err != nil {
		return customer.CustomerPreferences{}, fmt.Errorf("mongodb.PreferencesRepository.FindByCustomerID: %w", err)
	}
	return prefs, nil
}
