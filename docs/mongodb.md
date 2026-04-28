# MongoDB — Product Catalog & Customer Preferences

> This document explains **why** MongoDB is used for the Product Catalog, **how** the polyglot persistence pattern is implemented, and **what to expect** when operating the two bounded contexts together.

---

## Why MongoDB Here

The existing Customer aggregate is a perfect fit for PostgreSQL: fixed schema, referential integrity, relational queries. Forcing MongoDB onto Customer would be artificial.

The **Product Catalog** is the natural counterpart. Products have:

- **Variable schema by category** — Electronics have `voltage`, `warranty_months`, `connectivity`. Clothing has `sizes`, `colors`, `material`. Food has `allergens`, `nutritional_info`. A relational schema requires either a massive nullable table or EAV (Entity-Attribute-Value) — both are painful.
- **Nested documents** — A product has variants (size M in red, size L in blue). In Postgres this is a join; in MongoDB it is natural embedding.
- **Flexible text search** — Full-text search on product name and description with relevance scoring. MongoDB's text indexes and `$meta: textScore` handle this elegantly.
- **Rich read patterns, few writes** — Catalog is read-heavy. MongoDB's flexible projection and horizontal scaling fit well.

Additionally, this adds a **second bounded context**. The Customer context (Postgres) and Product context (MongoDB) coexist without sharing data stores:

```
Customer → Postgres  (relational, strong consistency, referential integrity)
Product  → MongoDB   (flexible schema, embedded documents, text search)
```

This is the **Polyglot Persistence** pattern — a real-world architectural decision every backend engineer will encounter.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                      cmd/api/main.go                          │
│               (Composition root — wires everything)           │
└──────────────┬───────────────────────────┬───────────────────┘
               │                           │
   ┌───────────▼──────────┐   ┌────────────▼──────────────────┐
   │  internal/customer   │   │  internal/product              │
   │  (Postgres)          │   │  (MongoDB)                     │
   │                      │   │                                │
   │  domain.go           │   │  domain.go                     │
   │  service.go          │   │  service.go                    │
   │  query.go            │   │  query.go                      │
   │  handler.go          │   │  handler.go                    │
   │  repository.go ←──┐  │   │  repository.go ←──┐           │
   │  preferences.go ←─┤  │   │                   │           │
   └──────────────────┼─┘   └───────────────────┼───────────┘
                      │                          │
         ┌────────────▼──────────────────────────▼────────────┐
         │           platform/database/mongodb/                │
         │   client.go         — NewClient, EnsureIndexes      │
         │   product_repo.go   — satisfies product.Repository  │
         │   preferences_repo.go — satisfies customer.Prefs.   │
         └────────────────────────┬───────────────────────────┘
                                  │
                             ┌────▼─────┐
                             │ MongoDB  │
                             │ :27017   │
                             └──────────┘
```

**Key invariant**: `internal/product` never imports `internal/customer` and vice versa. The handler layer is the only layer that can orchestrate across contexts.

---

## File Structure

```
internal/
├── product/
│   ├── domain.go             ← Product aggregate, Variant, Category, sentinel errors
│   ├── repository.go         ← Repository interface (consumer-side)
│   ├── service.go            ← Write: Create, Update, Archive
│   ├── query.go              ← Read: GetByID, Search, ListByCategory
│   ├── handler.go            ← Echo HTTP handlers + route registration
│   └── product_test.go       ← Unit tests (table-driven, mock repo)
│
├── customer/
│   ├── preferences.go        ← CustomerPreferences entity, PreferencesRepository interface
│   ├── preferences_service.go ← PreferencesService: Upsert, GetByCustomerID
│   └── preferences_handler.go ← Echo handlers for GET/PUT /customers/:id/preferences
│
└── platform/
    └── database/
        └── mongodb/
            ├── client.go          ← NewClient, EnsureIndexes
            ├── product_repo.go    ← ProductRepository (satisfies product.Repository)
            └── preferences_repo.go ← PreferencesRepository (satisfies customer.PreferencesRepository)
```

---

## Domain Design

### Product Aggregate

The `Product` struct uses a flexible `map[string]any` for the `Attributes` field. This is the core feature — different categories carry different attribute schemas, and MongoDB stores the map natively as a BSON sub-document.

```go
type Product struct {
    ID          bson.ObjectID  `bson:"_id,omitempty"`
    SKU         string         `bson:"sku"`
    Name        string         `bson:"name"`
    Description string         `bson:"description"`
    Category    Category       `bson:"category"`
    Attributes  map[string]any `bson:"attributes"`  // flexible per category
    Variants    []Variant      `bson:"variants"`     // embedded, no separate collection
    PriceCents  int64          `bson:"price_cents"`  // money as integer cents
    Active      bool           `bson:"active"`
    CreatedAt   time.Time      `bson:"created_at"`
    UpdatedAt   time.Time      `bson:"updated_at"`
}
```

`Variants` are embedded documents — not a separate collection. This avoids joins for the common "get product with all variants" read pattern.

`PriceCents` stores money as integer cents (e.g. $9.99 = 999) to avoid floating-point precision issues.

Product ID is a **MongoDB ObjectID** (24-character hex string), not a UUID. This is intentional — the Product bounded context owns its own ID scheme. Callers must not assume UUIDs.

### Customer Preferences

`CustomerPreferences` is stored in MongoDB and linked to the Postgres Customer by `CustomerID` (a UUID, not an ObjectID):

```go
type CustomerPreferences struct {
    CustomerID         uuid.UUID `bson:"customer_id"`
    FavoriteCategories []string  `bson:"favorite_categories"`
    WatchList          []string  `bson:"watchlist"`  // Product ObjectID hex strings
    UpdatedAt          time.Time `bson:"updated_at"`
}
```

This deliberately stores only the link — the Customer entity itself lives in Postgres. The two stores are not queried together via a join; the handler makes two separate calls when cross-context data is needed.

---

## MongoDB Indexes

Indexes are created at startup in `EnsureIndexes` — there is no migration file for MongoDB. The call is idempotent: existing indexes with the same name are skipped silently.

### `products` collection

| Index name | Keys | Properties |
|---|---|---|
| `sku_unique` | `sku: 1` | Unique — prevents duplicate SKUs |
| `category_active` | `category: 1, active: 1` | Compound — accelerates category+active queries |
| `product_text_search` | `name: "text", description: "text"` | Text — powers `$text` search; `name` weighted 10×, `description` 1× |

### `customer_preferences` collection

| Index name | Keys | Properties |
|---|---|---|
| `customer_id_unique` | `customer_id: 1` | Unique — one preferences document per customer |

---

## Text Search

`GET /api/v1/products?q=wireless+headphone` triggers a full-text search using MongoDB's `$text` operator:

```go
filter := bson.M{
    "$text":  bson.M{"$search": query},
    "active": true,
}
opts := options.Find().
    SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}})
```

Results are ordered by relevance score. The `name` field is weighted 10× so a name match outranks a description match at the same term frequency. Only `active: true` products are returned — archived products do not appear in search.

**Text search requires the `product_text_search` index.** Without it, `$text` queries fail. `EnsureIndexes` creates this index at startup.

---

## Attribute Filtering

`GET /api/v1/products?category=clothing&attributes[material]=cotton` filters by category and attribute simultaneously:

```
filter["attributes.material"] = "cotton"
```

MongoDB's dot notation (`attributes.material`) queries into the nested `Attributes` map without any schema definition. This is the primary advantage of using MongoDB for this context — attribute keys are dynamic and unknown at compile time.

---

## Soft Delete (Archive)

`DELETE /api/v1/products/:id` does not remove the document. It sets `active: false` and updates `updated_at`. Archived products:

- Are excluded from all `FindByCategory` queries (`active: true` filter)
- Are excluded from `Search` results (`active: true` filter)
- Are still retrievable by `GetByID` for audit purposes

This matches the `Archive()` method on the domain entity:

```go
func (p *Product) Archive() {
    p.Active = false
    p.UpdatedAt = time.Now().UTC()
}
```

---

## No Cross-Context DB Calls

The product and customer bounded contexts share no database calls. When a handler needs data from both contexts, it makes two separate calls:

```go
// Handler — the only layer that orchestrates across contexts
func (h *ProductHandler) GetRecommended(c echo.Context) error {
    customerID := auth.CustomerIDFromContext(c.Request().Context())

    // Two separate calls — no shared transaction, no shared DB
    prefs, _ := h.prefsQuery.FindByCustomerID(c.Request().Context(), customerID)
    products, err := h.productQuery.ListByCategories(c.Request().Context(), prefs.FavoriteCategories, 10)
    // ...
}
```

Never import `mongodb/` from `postgres/` or vice versa. Never import `product` from `customer` or vice versa. Services stay isolated; the handler is the integration point.

---

## API Reference

### Products

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/api/v1/products` | JWT | Create a product |
| `GET` | `/api/v1/products/:id` | JWT | Get product by ObjectID |
| `GET` | `/api/v1/products?category=&q=&limit=&offset=` | JWT | List by category or full-text search |
| `PUT` | `/api/v1/products/:id` | JWT | Update name, description, attributes, price |
| `DELETE` | `/api/v1/products/:id` | JWT | Archive (soft delete) |

### Customer Preferences

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/api/v1/customers/:id/preferences` | JWT | Get customer preferences |
| `PUT` | `/api/v1/customers/:id/preferences` | JWT | Upsert customer preferences |

Product ID is a **MongoDB ObjectID** — a 24-character hex string. The handler returns HTTP 400 if the format is invalid.

#### Create product request

```json
{
  "name": "Wireless Headphones",
  "description": "Noise-cancelling, Bluetooth 5.0",
  "sku": "WH-001",
  "category": "electronics",
  "attributes": {
    "voltage": "5V",
    "warranty_months": 24,
    "connectivity": ["bluetooth", "usb-c"]
  },
  "variants": [
    {
      "sku_suffix": "-BLK",
      "attributes": {"color": "black"},
      "stock_units": 50
    }
  ],
  "price_cents": 9999
}
```

#### Upsert preferences request

```json
{
  "favorite_categories": ["electronics", "books"],
  "watchlist": ["507f1f77bcf86cd799439011", "507f191e810c19729de860ea"]
}
```

---

## Configuration

| Variable | Default | Required | Description |
|---|---|---|---|
| `MONGO_URI` | `mongodb://localhost:27017` | No | MongoDB connection string |
| `MONGO_DATABASE` | `go_blueprint` | No | Database name |

When `MONGO_URI` is the default, the server connects to a local MongoDB instance. If connection or ping fails at startup, the process exits with code 1.

---

## Getting Started

```bash
# Start MongoDB alongside Postgres and Redis
docker compose up -d

# Confirm MongoDB is healthy
docker compose ps

# The server connects to MongoDB at startup
go run ./cmd/api
```

Expected startup log:

```
time=2026-04-27T12:00:00Z level=INFO msg="server starting" addr=:8080
```

The `EnsureIndexes` call runs silently after connection — if index creation fails, the server exits.

---

## Testing

### Unit tests

Unit tests mock the `product.Repository` interface. No Docker needed:

```bash
go test ./internal/product/... -race -count=1
```

Tests cover `NewProduct` (table-driven), `UpdateDetails`, `Archive`, `Service.Create`, `Service.Archive`, and `QueryService` limit clamping.

### Integration tests (`build tag: integration`)

Integration tests use `testcontainers-go` to spin up a real `mongo:7.0` instance:

```bash
go test ./... -tags=integration -race -count=1
```

Key integration test cases:
- Text search returns products ordered by relevance score
- Category filter combined with attribute filter
- Duplicate SKU returns `ErrSKUAlreadyExists`
- Archived product excluded from search results
- Customer preferences upsert is idempotent

---

## What NOT to Do

| Do not | Why |
|---|---|
| Import `platform/database/mongodb` from domain packages | Dependency arrows point inward; infra imports domain, not the reverse |
| Import `product` from `customer` or vice versa | Bounded contexts are independent — the handler orchestrates both |
| Use ObjectIDs as UUIDs or vice versa | Each context owns its own ID type; mixing them creates coupling |
| Query both Postgres and MongoDB in the same service method | Services are context-local; cross-context orchestration belongs in handlers |
| Edit `platform/database/mongodb` files to add business logic | These are adapters — business rules live in domain packages |
| Skip `EnsureIndexes` in tests that use real MongoDB | The text search index must exist for `$text` queries to work |
