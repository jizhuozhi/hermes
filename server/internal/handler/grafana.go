package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type GrafanaHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewGrafanaHandler(s store.Store, logger *zap.SugaredLogger) *GrafanaHandler {
	return &GrafanaHandler{store: s, logger: logger}
}

// ListDashboards returns all configured Grafana dashboards in the current namespace.
func (h *GrafanaHandler) ListDashboards(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	dashboards, err := h.store.ListGrafanaDashboards(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("list grafana dashboards: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dashboards == nil {
		dashboards = []store.GrafanaDashboard{}
	}
	JSON(w, http.StatusOK, map[string]any{"dashboards": dashboards})
}

// PutDashboard creates or updates a Grafana dashboard.
func (h *GrafanaHandler) PutDashboard(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	body, err := ReadBody(r)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var d store.GrafanaDashboard
	if err := json.Unmarshal(body, &d); err != nil {
		ErrJSON(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if d.Name == "" || d.URL == "" {
		ErrJSON(w, http.StatusBadRequest, "name and url are required")
		return
	}

	isNew := d.ID == 0

	result, err := h.store.PutGrafanaDashboard(r.Context(), ns, &d)
	if err != nil {
		h.logger.Errorf("put grafana dashboard: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	JSON(w, status, result)
}

// DeleteDashboard deletes a Grafana dashboard by ID.
func (h *GrafanaHandler) DeleteDashboard(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		ErrJSON(w, http.StatusBadRequest, "invalid dashboard id")
		return
	}

	if err := h.store.DeleteGrafanaDashboard(r.Context(), ns, id); err != nil {
		h.logger.Errorf("delete grafana dashboard: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
