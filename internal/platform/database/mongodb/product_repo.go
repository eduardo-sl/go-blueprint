package mongodb

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduardo-sl/go-blueprint/internal/product"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ProductRepository implements product.Repository using MongoDB.
type ProductRepository struct {
	col *mongo.Collection
}

func NewProductRepository(db *mongo.Database) *ProductRepository {
	return &ProductRepository{col: db.Collection("products")}
}

func (r *ProductRepository) Save(ctx context.Context, p product.Product) error {
	_, err := r.col.InsertOne(ctx, p)
	if mongo.IsDuplicateKeyError(err) {
		return product.ErrSKUAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("mongodb.ProductRepository.Save: %w", err)
	}
	return nil
}

func (r *ProductRepository) FindByID(ctx context.Context, id bson.ObjectID) (product.Product, error) {
	var p product.Product
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return product.Product{}, product.ErrProductNotFound
	}
	if err != nil {
		return product.Product{}, fmt.Errorf("mongodb.ProductRepository.FindByID: %w", err)
	}
	return p, nil
}

func (r *ProductRepository) FindBySKU(ctx context.Context, sku string) (product.Product, error) {
	var p product.Product
	err := r.col.FindOne(ctx, bson.M{"sku": sku}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return product.Product{}, product.ErrProductNotFound
	}
	if err != nil {
		return product.Product{}, fmt.Errorf("mongodb.ProductRepository.FindBySKU: %w", err)
	}
	return p, nil
}

func (r *ProductRepository) Update(ctx context.Context, p product.Product) error {
	_, err := r.col.ReplaceOne(ctx, bson.M{"_id": p.ID}, p)
	if err != nil {
		return fmt.Errorf("mongodb.ProductRepository.Update: %w", err)
	}
	return nil
}

// Search performs a full-text search on name and description using MongoDB's $text operator.
// Products are ordered by relevance score. Only active products are returned.
func (r *ProductRepository) Search(ctx context.Context, query string, category product.Category, limit int) ([]product.Product, error) {
	filter := bson.M{
		"$text":  bson.M{"$search": query},
		"active": true,
	}
	if category != "" {
		filter["category"] = category
	}

	opts := options.Find().
		SetLimit(int64(limit)).
		SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}})

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb.ProductRepository.Search: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []product.Product
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("mongodb.ProductRepository.Search: decode: %w", err)
	}
	return results, nil
}

// FindByCategory returns active products for a category with optional attribute filtering.
// attributeFilters keys are matched via MongoDB dot notation: {"material": "cotton"}
// maps to filter key "attributes.material" = "cotton".
func (r *ProductRepository) FindByCategory(
	ctx context.Context,
	category product.Category,
	attributeFilters map[string]string,
	limit, offset int,
) ([]product.Product, error) {
	filter := bson.M{"active": true}
	if category != "" {
		filter["category"] = category
	}
	for k, v := range attributeFilters {
		filter["attributes."+k] = v
	}

	opts := options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		SetSort(bson.D{{Key: "created_at", Value: -1}})

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb.ProductRepository.FindByCategory: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []product.Product
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("mongodb.ProductRepository.FindByCategory: decode: %w", err)
	}
	return results, nil
}
