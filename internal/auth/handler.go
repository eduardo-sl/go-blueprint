package auth

import (
	"errors"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

var _validate = validator.New()

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(g *echo.Group) {
	g.POST("/auth/register", h.Register)
	g.POST("/auth/login", h.Login)
}

// Register godoc
// @Summary     Register a new user
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       body body registerRequest true "Registration payload"
// @Success     201  {object} map[string]string
// @Failure     400  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Router      /auth/register [post]
func (h *Handler) Register(c echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	id, err := h.svc.Register(c.Request().Context(), RegisterCmd(req))
	if err != nil {
		return mapAuthError(err)
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": id.String()})
}

// Login godoc
// @Summary     Authenticate and get JWT token
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       body body loginRequest true "Login credentials"
// @Success     200  {object} TokenResponse
// @Failure     400  {object} map[string]string
// @Failure     401  {object} map[string]string
// @Router      /auth/login [post]
func (h *Handler) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := _validate.Struct(req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	token, err := h.svc.Login(c.Request().Context(), LoginCmd(req))
	if err != nil {
		return mapAuthError(err)
	}

	return c.JSON(http.StatusOK, token)
}

type registerRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Name     string `json:"name"     validate:"required,min=2"`
	Password string `json:"password" validate:"required,min=8"`
}

type loginRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

func mapAuthError(err error) error {
	switch {
	case errors.Is(err, ErrEmailExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidPassword):
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrUserNotFound):
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, ErrPasswordTooShort):
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
}
