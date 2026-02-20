package handler

import (
	"fmt"
	"net/http"

	"github.com/jizhuozhi/hermes/server/internal/model"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

// ConfigHandler handles config-level operations (get/put/validate).
// Named RouteHandler for backward compatibility with main.go references.
type RouteHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewRouteHandler(s store.Store, logger *zap.SugaredLogger) *RouteHandler {
	return &RouteHandler{store: s, logger: logger}
}

func (h *RouteHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	cfg, err := h.store.GetConfig(r.Context(), region)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]any{"config": cfg})
}

func (h *RouteHandler) PutConfig(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	var cfg model.GatewayConfig
	if err := DecodeJSON(r, &cfg); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	if errs := model.ValidateConfig(&cfg); len(errs) > 0 {
		JSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
		return
	}

	_, err := h.store.PutAllConfig(r.Context(), region, cfg.Domains, cfg.Clusters, Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]any{"domains": len(cfg.Domains), "clusters": len(cfg.Clusters)})
}

func (h *RouteHandler) ValidateConfig(w http.ResponseWriter, r *http.Request) {
	var cfg model.GatewayConfig
	if err := DecodeJSON(r, &cfg); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	if errs := model.ValidateConfig(&cfg); len(errs) > 0 {
		JSON(w, http.StatusOK, map[string]any{"valid": false, "errors": errs})
		return
	}
	JSON(w, http.StatusOK, map[string]any{"valid": true, "domains": len(cfg.Domains), "clusters": len(cfg.Clusters)})
}
