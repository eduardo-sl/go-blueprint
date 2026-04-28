package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// NewClient creates and validates a MongoDB connection.
// The caller is responsible for calling client.Disconnect(ctx) on shutdown.
func NewClient(ctx context.Context, uri string) (*mongo.Client, error) {
	opts := options.Client().
		ApplyURI(uri).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(10 * time.Second)

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb.NewClient: connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("mongodb.NewClient: ping: %w", err)
	}

	return client, nil
}

// EnsureIndexes creates all required indexes for the product catalog and preferences
// collections. The call is idempotent — existing indexes with the same name are not recreated.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	if err := ensureProductIndexes(ctx, db); err != nil {
		return err
	}
	return ensurePreferencesIndexes(ctx, db)
}

func ensureProductIndexes(ctx context.Context, db *mongo.Database) error {
	col := db.Collection("products")

	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "sku", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("sku_unique"),
		},
		{
			Keys:    bson.D{{Key: "category", Value: 1}, {Key: "active", Value: 1}},
			Options: options.Index().SetName("category_active"),
		},
		{
			// Text index for full-text search on name + description.
			// name matches score 10× more than description.
			Keys: bson.D{
				{Key: "name", Value: "text"},
				{Key: "description", Value: "text"},
			},
			Options: options.Index().
				SetName("product_text_search").
				SetWeights(bson.D{
					{Key: "name", Value: 10},
					{Key: "description", Value: 1},
				}),
		},
	}

	_, err := col.Indexes().CreateMany(ctx, indexes)
	if err != nil {
		return fmt.Errorf("mongodb.EnsureIndexes products: %w", err)
	}
	return nil
}

func ensurePreferencesIndexes(ctx context.Context, db *mongo.Database) error {
	col := db.Collection("customer_preferences")

	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "customer_id", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("customer_id_unique"),
	})
	if err != nil {
		return fmt.Errorf("mongodb.EnsureIndexes customer_preferences: %w", err)
	}
	return nil
}
