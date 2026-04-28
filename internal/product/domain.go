package product

import (
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Product is the aggregate root for the catalog.
// Attributes varies by Category and is stored as flexible BSON.
type Product struct {
	ID          bson.ObjectID          `bson:"_id,omitempty"`
	SKU         string                 `bson:"sku"`
	Name        string                 `bson:"name"`
	Description string                 `bson:"description"`
	Category    Category               `bson:"category"`
	Attributes  map[string]any `bson:"attributes"`
	Variants    []Variant              `bson:"variants"`
	PriceCents  int64                  `bson:"price_cents"`
	Active      bool                   `bson:"active"`
	CreatedAt   time.Time              `bson:"created_at"`
	UpdatedAt   time.Time              `bson:"updated_at"`
}

type Category string

const (
	CategoryElectronics Category = "electronics"
	CategoryClothing    Category = "clothing"
	CategoryFood        Category = "food"
	CategoryBooks       Category = "books"
)

// Variant is embedded inside the Product document — no separate collection.
type Variant struct {
	ID         string            `bson:"id"`
	Attributes map[string]string `bson:"attributes"`
	SKUSuffix  string            `bson:"sku_suffix"`
	StockUnits int               `bson:"stock_units"`
	Active     bool              `bson:"active"`
}

// categoryAttributes defines which attribute keys are expected per category.
// Unknown keys are permitted — the schema is intentionally flexible.
var categoryAttributes = map[Category][]string{
	CategoryElectronics: {"voltage", "warranty_months", "connectivity"},
	CategoryClothing:    {"material", "sizes", "colors", "gender"},
	CategoryFood:        {"allergens", "weight_grams", "nutritional_info"},
	CategoryBooks:       {"author", "isbn", "pages", "language"},
}

var (
	ErrProductNotFound  = errors.New("product not found")
	ErrSKUAlreadyExists = errors.New("SKU already registered")
	ErrInvalidCategory  = errors.New("invalid product category")
	ErrNoVariants       = errors.New("product must have at least one variant")
	ErrNameRequired     = errors.New("product name is required")
	ErrPriceNegative    = errors.New("price cannot be negative")
)

func NewProduct(
	name, description, sku string,
	category Category,
	attrs map[string]any,
	variants []Variant,
	priceCents int64,
) (Product, error) {
	if name == "" {
		return Product{}, ErrNameRequired
	}
	if _, ok := categoryAttributes[category]; !ok {
		return Product{}, ErrInvalidCategory
	}
	if len(variants) == 0 {
		return Product{}, ErrNoVariants
	}
	if priceCents < 0 {
		return Product{}, ErrPriceNegative
	}

	now := time.Now().UTC()
	return Product{
		ID:          bson.NewObjectID(),
		SKU:         sku,
		Name:        name,
		Description: description,
		Category:    category,
		Attributes:  attrs,
		Variants:    variants,
		PriceCents:  priceCents,
		Active:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (p *Product) UpdateDetails(name, description string, attrs map[string]any, priceCents int64) error {
	if name == "" {
		return ErrNameRequired
	}
	if priceCents < 0 {
		return ErrPriceNegative
	}
	p.Name = name
	p.Description = description
	p.Attributes = attrs
	p.PriceCents = priceCents
	p.UpdatedAt = time.Now().UTC()
	return nil
}

func (p *Product) Archive() {
	p.Active = false
	p.UpdatedAt = time.Now().UTC()
}
