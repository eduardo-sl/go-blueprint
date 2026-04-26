package customer

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

var _validate = validator.New()

type Handler struct {
	svc   *Service
	query *QueryService
}

func NewHandler(svc *Service, query *QueryService) *Handler {
	return &Handler{svc: svc, query: query}
}

func (h *Handler) RegisterRoutes(g *echo.Group) {
	g.POST("/customers", h.Register)
	g.PUT("/customers/:id", h.Update)
	g.DELETE("/customers/:id", h.Remove)
	g.GET("/customers/:id", h.GetByID)
	g.GET("/customers", h.List)
}

// Register godoc
// @Summary     Register a new customer
// @Tags        customers
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body registerRequest true "Customer data"
// @Success     201  {object} map[string]string
// @Failure     400  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Failure     422  {object} map[string]string
// @Router      /customers [post]
func (h *Handler) Register(c echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	birthDate, err := time.Parse("2006-01-02", req.BirthDate)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "birth_date must be in YYYY-MM-DD format")
	}

	id, err := h.svc.Register(c.Request().Context(), RegisterCmd{
		Name:      req.Name,
		Email:     req.Email,
		BirthDate: birthDate,
	})
	if err != nil {
		return mapDomainError(err)
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": id.String()})
}

// Update godoc
// @Summary     Update a customer
// @Tags        customers
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string          true "Customer ID"
// @Param       body body updateRequest   true "Customer data"
// @Success     204
// @Failure     400  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Router      /customers/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid customer ID")
	}

	var req updateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	birthDate, err := time.Parse("2006-01-02", req.BirthDate)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "birth_date must be in YYYY-MM-DD format")
	}

	if err := h.svc.Update(c.Request().Context(), UpdateCmd{
		ID:        id,
		Name:      req.Name,
		Email:     req.Email,
		BirthDate: birthDate,
	}); err != nil {
		return mapDomainError(err)
	}

	return c.NoContent(http.StatusNoContent)
}

// Remove godoc
// @Summary     Remove a customer
// @Tags        customers
// @Security    BearerAuth
// @Param       id path string true "Customer ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Router      /customers/{id} [delete]
func (h *Handler) Remove(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid customer ID")
	}

	if err := h.svc.Remove(c.Request().Context(), id); err != nil {
		return mapDomainError(err)
	}

	return c.NoContent(http.StatusNoContent)
}

// GetByID godoc
// @Summary     Get customer by ID
// @Tags        customers
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Customer ID"
// @Success     200 {object} customerResponse
// @Failure     404 {object} map[string]string
// @Router      /customers/{id} [get]
func (h *Handler) GetByID(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid customer ID")
	}

	cust, err := h.query.GetByID(c.Request().Context(), id)
	if err != nil {
		return mapDomainError(err)
	}

	return c.JSON(http.StatusOK, toResponse(cust))
}

// List godoc
// @Summary     List all customers
// @Tags        customers
// @Produce     json
// @Security    BearerAuth
// @Success     200 {array}  customerResponse
// @Router      /customers [get]
func (h *Handler) List(c echo.Context) error {
	customers, err := h.query.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	res := make([]customerResponse, len(customers))
	for i, cust := range customers {
		res[i] = toResponse(cust)
	}

	return c.JSON(http.StatusOK, res)
}

type registerRequest struct {
	Name      string `json:"name"       validate:"required,min=2"`
	Email     string `json:"email"      validate:"required,email"`
	BirthDate string `json:"birth_date" validate:"required"`
}

type updateRequest struct {
	Name      string `json:"name"       validate:"required,min=2"`
	Email     string `json:"email"      validate:"required,email"`
	BirthDate string `json:"birth_date" validate:"required"`
}

type customerResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	BirthDate string `json:"birth_date"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toResponse(c Customer) customerResponse {
	return customerResponse{
		ID:        c.ID.String(),
		Name:      c.Name,
		Email:     c.Email,
		BirthDate: c.BirthDate.Format("2006-01-02"),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}

func mapDomainError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, ErrEmailExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidBirthDate):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
}
