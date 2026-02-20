package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/model"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type ClusterHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewClusterHandler(s store.Store, logger *zap.SugaredLogger) *ClusterHandler {
	return &ClusterHandler{store: s, logger: logger}
}

func (h *ClusterHandler) ListClusters(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	clusters, err := h.store.ListClusters(r.Context(), region)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"clusters": clusters, "total": len(clusters)})
}

func (h *ClusterHandler) GetCluster(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")
	cluster, rv, err := h.store.GetCluster(r.Context(), region, name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cluster == nil {
		ErrJSON(w, http.StatusNotFound, fmt.Sprintf("cluster %q not found", name))
		return
	}
	JSON(w, http.StatusOK, map[string]any{"cluster": cluster, "resource_version": rv})
}

func (h *ClusterHandler) CreateCluster(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	var cluster model.ClusterConfig
	if err := DecodeJSON(r, &cluster); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	if cluster.Name == "" {
		ErrJSON(w, http.StatusBadRequest, "cluster name is required")
		return
	}

	if errs := model.ValidateCluster(&cluster); len(errs) > 0 {
		JSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
		return
	}

	ver, err := h.store.PutCluster(r.Context(), region, &cluster, "create", Operator(r), 0)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			ErrJSON(w, http.StatusConflict, fmt.Sprintf("cluster %q already exists", cluster.Name))
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("cluster created: %s (ns=%s), version=%d", cluster.Name, region, ver)
	JSON(w, http.StatusCreated, map[string]any{"version": ver, "cluster": cluster, "resource_version": int64(1)})
}

func (h *ClusterHandler) UpdateCluster(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")

	var body struct {
		model.ClusterConfig
		ResourceVersion int64 `json:"resource_version"`
	}
	if err := DecodeJSON(r, &body); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	if body.ResourceVersion <= 0 {
		ErrJSON(w, http.StatusBadRequest, "resource_version is required for update (must be > 0)")
		return
	}

	body.ClusterConfig.Name = name

	if errs := model.ValidateCluster(&body.ClusterConfig); len(errs) > 0 {
		JSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
		return
	}

	ver, err := h.store.PutCluster(r.Context(), region, &body.ClusterConfig, "update", Operator(r), body.ResourceVersion)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			ErrJSON(w, http.StatusConflict, "conflict: the cluster has been modified by another user, please refresh and try again")
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("cluster updated: %s (ns=%s), version=%d", name, region, ver)
	JSON(w, http.StatusOK, map[string]any{"version": ver, "cluster": body.ClusterConfig, "resource_version": body.ResourceVersion + 1})
}

func (h *ClusterHandler) DeleteCluster(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")

	ver, err := h.store.DeleteCluster(r.Context(), region, name, Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("cluster deleted: %s (ns=%s), version=%d", name, region, ver)
	JSON(w, http.StatusOK, map[string]any{"version": ver})
}

// Per-cluster history & rollback
func (h *ClusterHandler) ListClusterHistory(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")
	history, err := h.store.GetClusterHistory(r.Context(), region, name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"history": history, "total": len(history)})
}

func (h *ClusterHandler) GetClusterVersion(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")
	versionStr := r.PathValue("version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid version: %v", err))
		return
	}

	entry, err := h.store.GetClusterVersion(r.Context(), region, name, version)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		ErrJSON(w, http.StatusNotFound, fmt.Sprintf("cluster %q version %d not found", name, version))
		return
	}

	JSON(w, http.StatusOK, entry)
}

func (h *ClusterHandler) RollbackCluster(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	name := r.PathValue("name")
	versionStr := r.PathValue("version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid version: %v", err))
		return
	}

	newVersion, err := h.store.RollbackCluster(r.Context(), region, name, version, Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("cluster %s (ns=%s) rollback to version %d, new version=%d", name, region, version, newVersion)
	JSON(w, http.StatusOK, map[string]any{
		"name":           name,
		"rolled_back_to": version,
		"new_version":    newVersion,
	})
}
