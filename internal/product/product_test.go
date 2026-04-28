package product_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/eduardo-sl/go-blueprint/internal/product"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- NewProduct tests ---

func TestNewProduct(t *testing.T) {
	t.Parallel()

	validVariants := []product.Variant{
		{ID: "v1", Attributes: map[string]string{"size": "M"}, SKUSuffix: "-M", StockUnits: 10, Active: true},
	}

	tests := []struct {
		name        string
		pName       string
		description string
		sku         string
		category    product.Category
		attrs       map[string]any
		variants    []product.Variant
		priceCents  int64
		wantErr     error
	}{
		{
			name:       "valid electronics product",
			pName:      "Wireless Headphones",
			sku:        "WH-001",
			category:   product.CategoryElectronics,
			attrs:      map[string]any{"voltage": "5V"},
			variants:   validVariants,
			priceCents: 9999,
		},
		{
			name:       "valid clothing product",
			pName:      "Cotton T-Shirt",
			sku:        "TS-001",
			category:   product.CategoryClothing,
			attrs:      map[string]any{"material": "cotton"},
			variants:   validVariants,
			priceCents: 2999,
		},
		{
			name:      "empty name",
			sku:       "X-001",
			category:  product.CategoryBooks,
			variants:  validVariants,
			wantErr:   product.ErrNameRequired,
		},
		{
			name:     "invalid category",
			pName:    "Unknown",
			sku:      "X-001",
			category: product.Category("gadgets"),
			variants: validVariants,
			wantErr:  product.ErrInvalidCategory,
		},
		{
			name:       "no variants",
			pName:      "Item",
			sku:        "X-001",
			category:   product.CategoryFood,
			variants:   nil,
			priceCents: 100,
			wantErr:    product.ErrNoVariants,
		},
		{
			name:       "negative price",
			pName:      "Item",
			sku:        "X-001",
			category:   product.CategoryFood,
			variants:   validVariants,
			priceCents: -1,
			wantErr:    product.ErrPriceNegative,
		},
		{
			name:       "zero price allowed",
			pName:      "Free Item",
			sku:        "FREE-001",
			category:   product.CategoryBooks,
			variants:   validVariants,
			priceCents: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := product.NewProduct(tc.pName, tc.description, tc.sku, tc.category, tc.attrs, tc.variants, tc.priceCents)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr), "expected %v, got %v", tc.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.pName, p.Name)
			assert.Equal(t, tc.sku, p.SKU)
			assert.Equal(t, tc.category, p.Category)
			assert.Equal(t, tc.priceCents, p.PriceCents)
			assert.True(t, p.Active)
			assert.False(t, p.ID.IsZero())
			assert.False(t, p.CreatedAt.IsZero())
		})
	}
}

func TestProduct_UpdateDetails(t *testing.T) {
	t.Parallel()

	p, err := product.NewProduct("Original", "desc", "SKU-1", product.CategoryElectronics,
		map[string]any{"voltage": "5V"},
		[]product.Variant{{ID: "v1", Attributes: map[string]string{"size": "M"}, Active: true}},
		1000,
	)
	require.NoError(t, err)

	t.Run("valid update", func(t *testing.T) {
		err := p.UpdateDetails("Updated Name", "new desc", map[string]any{"voltage": "12V"}, 2000)
		require.NoError(t, err)
		assert.Equal(t, "Updated Name", p.Name)
		assert.Equal(t, int64(2000), p.PriceCents)
	})

	t.Run("empty name rejected", func(t *testing.T) {
		err := p.UpdateDetails("", "desc", nil, 1000)
		assert.ErrorIs(t, err, product.ErrNameRequired)
	})

	t.Run("negative price rejected", func(t *testing.T) {
		err := p.UpdateDetails("Name", "desc", nil, -1)
		assert.ErrorIs(t, err, product.ErrPriceNegative)
	})
}

func TestProduct_Archive(t *testing.T) {
	t.Parallel()

	p, err := product.NewProduct("Item", "desc", "SKU-1", product.CategoryFood,
		nil,
		[]product.Variant{{ID: "v1", Active: true}},
		500,
	)
	require.NoError(t, err)
	require.True(t, p.Active)

	p.Archive()
	assert.False(t, p.Active)
}

// --- Service tests ---

type mockRepo struct {
	saved    *product.Product
	findByID func(id bson.ObjectID) (product.Product, error)
	updated  *product.Product
}

func (m *mockRepo) Save(_ context.Context, p product.Product) error {
	m.saved = &p
	return nil
}

func (m *mockRepo) FindByID(_ context.Context, id bson.ObjectID) (product.Product, error) {
	if m.findByID != nil {
		return m.findByID(id)
	}
	return product.Product{}, product.ErrProductNotFound
}

func (m *mockRepo) FindBySKU(_ context.Context, _ string) (product.Product, error) {
	return product.Product{}, product.ErrProductNotFound
}

func (m *mockRepo) Update(_ context.Context, p product.Product) error {
	m.updated = &p
	return nil
}

func (m *mockRepo) Search(_ context.Context, _ string, _ product.Category, _ int) ([]product.Product, error) {
	return nil, nil
}

func (m *mockRepo) FindByCategory(_ context.Context, _ product.Category, _ map[string]string, _, _ int) ([]product.Product, error) {
	return nil, nil
}

func TestService_Create(t *testing.T) {
	t.Parallel()

	repo := &mockRepo{}
	svc := product.NewService(repo, noopLogger())

	id, err := svc.Create(context.Background(), product.CreateCmd{
		Name:       "Laptop",
		SKU:        "LP-001",
		Category:   product.CategoryElectronics,
		Attributes: map[string]any{"voltage": "12V"},
		Variants: []product.Variant{
			{ID: "v1", Attributes: map[string]string{"storage": "512GB"}, Active: true},
		},
		PriceCents: 99900,
	})

	require.NoError(t, err)
	assert.False(t, id.IsZero())
	require.NotNil(t, repo.saved)
	assert.Equal(t, "Laptop", repo.saved.Name)
}

func TestService_Create_DomainError(t *testing.T) {
	t.Parallel()

	repo := &mockRepo{}
	svc := product.NewService(repo, noopLogger())

	_, err := svc.Create(context.Background(), product.CreateCmd{
		Name:     "item",
		SKU:      "X",
		Category: product.Category("invalid"),
		Variants: []product.Variant{{ID: "v1"}},
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, product.ErrInvalidCategory))
}

func TestService_Archive(t *testing.T) {
	t.Parallel()

	existingProduct, _ := product.NewProduct("Item", "", "SKU-1", product.CategoryFood,
		nil,
		[]product.Variant{{ID: "v1", Active: true}},
		500,
	)

	repo := &mockRepo{
		findByID: func(_ bson.ObjectID) (product.Product, error) {
			return existingProduct, nil
		},
	}
	svc := product.NewService(repo, noopLogger())

	err := svc.Archive(context.Background(), existingProduct.ID)
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	assert.False(t, repo.updated.Active)
}

func TestService_Archive_NotFound(t *testing.T) {
	t.Parallel()

	repo := &mockRepo{}
	svc := product.NewService(repo, noopLogger())

	err := svc.Archive(context.Background(), bson.NewObjectID())
	assert.ErrorIs(t, err, product.ErrProductNotFound)
}

// --- QueryService tests ---

func TestQueryService_GetByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := &mockRepo{}
	qs := product.NewQueryService(repo)

	_, err := qs.GetByID(context.Background(), bson.NewObjectID())
	assert.ErrorIs(t, err, product.ErrProductNotFound)
}

func TestQueryService_ListByCategory_ClampLimit(t *testing.T) {
	t.Parallel()

	called := false
	customRepo := &clampCheckRepo{called: &called}
	qs := product.NewQueryService(customRepo)

	_, err := qs.ListByCategory(context.Background(), product.CategoryClothing, nil, 0, 0)
	require.NoError(t, err)
	assert.True(t, called, "FindByCategory should be called")
	assert.Equal(t, 20, customRepo.lastLimit)
}

type clampCheckRepo struct {
	called    *bool
	lastLimit int
}

func (r *clampCheckRepo) Save(_ context.Context, _ product.Product) error { return nil }
func (r *clampCheckRepo) FindByID(_ context.Context, _ bson.ObjectID) (product.Product, error) {
	return product.Product{}, nil
}
func (r *clampCheckRepo) FindBySKU(_ context.Context, _ string) (product.Product, error) {
	return product.Product{}, nil
}
func (r *clampCheckRepo) Update(_ context.Context, _ product.Product) error { return nil }
func (r *clampCheckRepo) Search(_ context.Context, _ string, _ product.Category, _ int) ([]product.Product, error) {
	return nil, nil
}
func (r *clampCheckRepo) FindByCategory(_ context.Context, _ product.Category, _ map[string]string, limit, _ int) ([]product.Product, error) {
	*r.called = true
	r.lastLimit = limit
	return nil, nil
}

// noopLogger returns a logger that discards all output in tests.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
