package customer

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type PreferencesHandler struct {
	svc *PreferencesService
}

func NewPreferencesHandler(svc *PreferencesService) *PreferencesHandler {
	return &PreferencesHandler{svc: svc}
}

// RegisterPreferencesRoutes registers preference routes onto an existing group.
// Called from server.Start so the routes share the same JWT-protected group as customers.
func (h *PreferencesHandler) RegisterPreferencesRoutes(g *echo.Group) {
	g.GET("/customers/:id/preferences", h.GetPreferences)
	g.PUT("/customers/:id/preferences", h.UpsertPreferences)
}

// GetPreferences godoc
// @Summary     Get customer preferences
// @Tags        preferences
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Customer ID"
// @Success     200 {object} preferencesResponse
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /customers/{id}/preferences [get]
func (h *PreferencesHandler) GetPreferences(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid customer ID")
	}

	prefs, err := h.svc.GetByCustomerID(c.Request().Context(), customerID)
	if err != nil {
		return mapPreferencesError(err)
	}

	return c.JSON(http.StatusOK, toPreferencesResponse(prefs))
}

// UpsertPreferences godoc
// @Summary     Upsert customer preferences
// @Tags        preferences
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string              true "Customer ID"
// @Param       body body preferencesRequest  true "Preferences data"
// @Success     204
// @Failure     400 {object} map[string]string
// @Router      /customers/{id}/preferences [put]
func (h *PreferencesHandler) UpsertPreferences(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid customer ID")
	}

	var req preferencesRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.svc.Upsert(c.Request().Context(), UpsertPreferencesCmd{
		CustomerID:         customerID,
		FavoriteCategories: req.FavoriteCategories,
		WatchList:          req.WatchList,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.NoContent(http.StatusNoContent)
}

func mapPreferencesError(err error) error {
	if errors.Is(err, ErrPreferencesNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
}

type preferencesRequest struct {
	FavoriteCategories []string `json:"favorite_categories"`
	WatchList          []string `json:"watchlist"`
}

type preferencesResponse struct {
	CustomerID         string   `json:"customer_id"`
	FavoriteCategories []string `json:"favorite_categories"`
	WatchList          []string `json:"watchlist"`
	UpdatedAt          string   `json:"updated_at"`
}

func toPreferencesResponse(p CustomerPreferences) preferencesResponse {
	cats := p.FavoriteCategories
	if cats == nil {
		cats = []string{}
	}
	wl := p.WatchList
	if wl == nil {
		wl = []string{}
	}
	return preferencesResponse{
		CustomerID:         p.CustomerID.String(),
		FavoriteCategories: cats,
		WatchList:          wl,
		UpdatedAt:          p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
