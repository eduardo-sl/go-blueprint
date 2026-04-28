package product

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var _validate = validator.New()

// productQuerier is the read-side interface consumed by Handler.
type productQuerier interface {
	GetByID(ctx context.Context, id bson.ObjectID) (Product, error)
	Search(ctx context.Context, query string, category Category, limit int) ([]Product, error)
	ListByCategory(ctx context.Context, category Category, attributeFilters map[string]string, limit, offset int) ([]Product, error)
}

type Handler struct {
	svc   *Service
	query productQuerier
}

func NewHandler(svc *Service, query productQuerier) *Handler {
	return &Handler{svc: svc, query: query}
}

func (h *Handler) RegisterRoutes(g *echo.Group) {
	g.GET("/products/:id", h.GetByID)
	g.GET("/products", h.ListOrSearch)
	g.POST("/products", h.Create)
	g.PUT("/products/:id", h.Update)
	g.DELETE("/products/:id", h.Archive)
}

// Create godoc
// @Summary     Create a product
// @Tags        products
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body createRequest true "Product data"
// @Success     201  {object} map[string]string
// @Failure     400  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Failure     422  {object} map[string]string
// @Router      /products [post]
func (h *Handler) Create(c echo.Context) error {
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	id, err := h.svc.Create(c.Request().Context(), CreateCmd{
		Name:        req.Name,
		Description: req.Description,
		SKU:         req.SKU,
		Category:    Category(req.Category),
		Attributes:  req.Attributes,
		Variants:    toVariants(req.Variants),
		PriceCents:  req.PriceCents,
	})
	if err != nil {
		return mapDomainError(err)
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": id.Hex()})
}

// GetByID godoc
// @Summary     Get product by ID
// @Tags        products
// @Produce     json
// @Param       id path string true "Product ID (MongoDB ObjectID hex)"
// @Success     200 {object} productResponse
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /products/{id} [get]
func (h *Handler) GetByID(c echo.Context) error {
	id, err := parseObjectID(c.Param("id"))
	if err != nil {
		return err
	}

	p, err := h.query.GetByID(c.Request().Context(), id)
	if err != nil {
		return mapDomainError(err)
	}

	return c.JSON(http.StatusOK, toProductResponse(p))
}

// ListOrSearch godoc
// @Summary     List or search products
// @Tags        products
// @Produce     json
// @Param       q        query string false "Full-text search query"
// @Param       category query string false "Filter by category"
// @Param       limit    query int    false "Max results (default 20, max 100)"
// @Param       offset   query int    false "Pagination offset"
// @Success     200 {array}  productResponse
// @Router      /products [get]
func (h *Handler) ListOrSearch(c echo.Context) error {
	q := c.QueryParam("q")
	categoryStr := c.QueryParam("category")
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))

	var products []Product
	var err error

	if q != "" {
		products, err = h.query.Search(c.Request().Context(), q, Category(categoryStr), limit)
	} else {
		attrs := parseAttributeFilters(c)
		products, err = h.query.ListByCategory(c.Request().Context(), Category(categoryStr), attrs, limit, offset)
	}

	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	res := make([]productResponse, len(products))
	for i, p := range products {
		res[i] = toProductResponse(p)
	}
	return c.JSON(http.StatusOK, res)
}

// Update godoc
// @Summary     Update a product
// @Tags        products
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string        true "Product ID (MongoDB ObjectID hex)"
// @Param       body body updateRequest true "Product data"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /products/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	id, err := parseObjectID(c.Param("id"))
	if err != nil {
		return err
	}

	var req updateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	if err := h.svc.Update(c.Request().Context(), UpdateCmd{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Attributes:  req.Attributes,
		PriceCents:  req.PriceCents,
	}); err != nil {
		return mapDomainError(err)
	}

	return c.NoContent(http.StatusNoContent)
}

// Archive godoc
// @Summary     Archive a product (soft delete)
// @Tags        products
// @Security    BearerAuth
// @Param       id path string true "Product ID (MongoDB ObjectID hex)"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /products/{id} [delete]
func (h *Handler) Archive(c echo.Context) error {
	id, err := parseObjectID(c.Param("id"))
	if err != nil {
		return err
	}

	if err := h.svc.Archive(c.Request().Context(), id); err != nil {
		return mapDomainError(err)
	}

	return c.NoContent(http.StatusNoContent)
}

func parseObjectID(s string) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(s)
	if err != nil {
		return bson.NilObjectID, echo.NewHTTPError(http.StatusBadRequest, "invalid product ID: must be a 24-character hex string")
	}
	return id, nil
}

// parseAttributeFilters reads query params of the form attributes[key]=value.
func parseAttributeFilters(c echo.Context) map[string]string {
	result := make(map[string]string)
	for k, v := range c.QueryParams() {
		if len(k) > 12 && k[:11] == "attributes[" && k[len(k)-1] == ']' {
			attrKey := k[11 : len(k)-1]
			if len(v) > 0 {
				result[attrKey] = v[0]
			}
		}
	}
	return result
}

func mapDomainError(err error) error {
	switch {
	case errors.Is(err, ErrProductNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, ErrSKUAlreadyExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidCategory):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrNoVariants):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrNameRequired):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrPriceNegative):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
}

type variantRequest struct {
	Attributes map[string]string `json:"attributes"`
	SKUSuffix  string            `json:"sku_suffix"`
	StockUnits int               `json:"stock_units"`
}

type createRequest struct {
	Name        string            `json:"name"        validate:"required,min=2"`
	Description string            `json:"description"`
	SKU         string            `json:"sku"         validate:"required"`
	Category    string            `json:"category"    validate:"required"`
	Attributes  map[string]any    `json:"attributes"`
	Variants    []variantRequest  `json:"variants"    validate:"required,min=1"`
	PriceCents  int64             `json:"price_cents" validate:"min=0"`
}

type updateRequest struct {
	Name        string         `json:"name"        validate:"required,min=2"`
	Description string         `json:"description"`
	Attributes  map[string]any `json:"attributes"`
	PriceCents  int64          `json:"price_cents" validate:"min=0"`
}

type variantResponse struct {
	ID         string            `json:"id"`
	Attributes map[string]string `json:"attributes"`
	SKUSuffix  string            `json:"sku_suffix"`
	StockUnits int               `json:"stock_units"`
	Active     bool              `json:"active"`
}

type productResponse struct {
	ID          string            `json:"id"`
	SKU         string            `json:"sku"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Attributes  map[string]any    `json:"attributes"`
	Variants    []variantResponse `json:"variants"`
	PriceCents  int64             `json:"price_cents"`
	Active      bool              `json:"active"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

func toProductResponse(p Product) productResponse {
	variants := make([]variantResponse, len(p.Variants))
	for i, v := range p.Variants {
		variants[i] = variantResponse{
			ID:         v.ID,
			Attributes: v.Attributes,
			SKUSuffix:  v.SKUSuffix,
			StockUnits: v.StockUnits,
			Active:     v.Active,
		}
	}
	return productResponse{
		ID:          p.ID.Hex(),
		SKU:         p.SKU,
		Name:        p.Name,
		Description: p.Description,
		Category:    string(p.Category),
		Attributes:  p.Attributes,
		Variants:    variants,
		PriceCents:  p.PriceCents,
		Active:      p.Active,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339),
	}
}

func toVariants(reqs []variantRequest) []Variant {
	variants := make([]Variant, len(reqs))
	for i, r := range reqs {
		variants[i] = Variant{
			ID:         bson.NewObjectID().Hex(),
			Attributes: r.Attributes,
			SKUSuffix:  r.SKUSuffix,
			StockUnits: r.StockUnits,
			Active:     true,
		}
	}
	return variants
}
