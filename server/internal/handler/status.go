package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type StatusHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewStatusHandler(s store.Store, logger *zap.SugaredLogger) *StatusHandler {
	return &StatusHandler{store: s, logger: logger}
}

// ReportInstances accepts a PUT/POST from the controller with the current
// list of gateway instances observed from etcd /hermes/instances.
func (h *StatusHandler) ReportInstances(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	body, err := ReadBody(r)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var report struct {
		Instances []store.GatewayInstanceStatus `json:"instances"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		ErrJSON(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}

	if err := h.store.UpsertGatewayInstances(r.Context(), ns, report.Instances); err != nil {
		h.logger.Errorf("upsert gateway instances: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "store: "+err.Error())
		return
	}

	h.logger.Infof("instances reported: ns=%s count=%d", ns, len(report.Instances))
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// AggregateStatus returns the current gateway instance list, controller status and metadata.
func (h *StatusHandler) AggregateStatus(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	instances, err := h.store.ListGatewayInstances(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("list instances: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if instances == nil {
		instances = []store.GatewayInstanceStatus{}
	}

	ctrl, err := h.store.GetControllerStatus(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("get controller: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := map[string]any{
		"instances": instances,
		"total":     len(instances),
	}

	if ctrl != nil {
		result["controller"] = ctrl
		result["updated_at"] = ctrl.UpdatedAt
	}

	JSON(w, http.StatusOK, result)
}

// ListInstances returns the raw instance list.
func (h *StatusHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	instances, err := h.store.ListGatewayInstances(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("list instances: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if instances == nil {
		instances = []store.GatewayInstanceStatus{}
	}

	JSON(w, http.StatusOK, map[string]any{"instances": instances})
}

// ReportController accepts a PUT from the controller with its own status.
func (h *StatusHandler) ReportController(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	body, err := ReadBody(r)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var report store.ControllerStatus
	if err := json.Unmarshal(body, &report); err != nil {
		ErrJSON(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}

	if err := h.store.UpsertControllerStatus(r.Context(), ns, &report); err != nil {
		h.logger.Errorf("upsert controller status: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "store: "+err.Error())
		return
	}

	h.logger.Infof("controller status reported: ns=%s id=%s status=%s revision=%d", ns, report.ID, report.Status, report.ConfigRevision)
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetController returns the current controller status.
func (h *StatusHandler) GetController(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	ctrl, err := h.store.GetControllerStatus(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("get controller: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	if ctrl == nil {
		JSON(w, http.StatusOK, map[string]any{"controller": nil})
		return
	}

	JSON(w, http.StatusOK, map[string]any{"controller": ctrl})
}
