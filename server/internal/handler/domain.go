package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/model"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type DomainHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewDomainHandler(s store.Store, logger *zap.SugaredLogger) *DomainHandler {
	return &DomainHandler{store: s, logger: logger}
}

func (h *DomainHandler) ListDomains(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	domains, err := h.store.ListDomains(r.Context(), ns)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"domains": domains, "total": len(domains)})
}

func (h *DomainHandler) GetDomain(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")
	domain, err := h.store.GetDomain(r.Context(), ns, name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if domain == nil {
		ErrJSON(w, http.StatusNotFound, fmt.Sprintf("domain %q not found", name))
		return
	}
	JSON(w, http.StatusOK, domain)
}

func (h *DomainHandler) CreateDomain(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	var domain model.DomainConfig
	if err := DecodeJSON(r, &domain); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	if domain.Name == "" {
		ErrJSON(w, http.StatusBadRequest, "domain name is required")
		return
	}

	if errs := model.ValidateDomain(&domain, nil); len(errs) > 0 {
		JSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
		return
	}

	existing, err := h.store.GetDomain(r.Context(), ns, domain.Name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil {
		ErrJSON(w, http.StatusConflict, fmt.Sprintf("domain %q already exists", domain.Name))
		return
	}

	ver, err := h.store.PutDomain(r.Context(), ns, &domain, "create", Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("domain created: %s (ns=%s), version=%d", domain.Name, ns, ver)
	JSON(w, http.StatusCreated, map[string]any{"version": ver, "domain": domain})
}

func (h *DomainHandler) UpdateDomain(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")

	var domain model.DomainConfig
	if err := DecodeJSON(r, &domain); err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	existing, err := h.store.GetDomain(r.Context(), ns, name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		ErrJSON(w, http.StatusNotFound, fmt.Sprintf("domain %q not found", name))
		return
	}

	domain.Name = name

	if errs := model.ValidateDomain(&domain, nil); len(errs) > 0 {
		JSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
		return
	}

	ver, err := h.store.PutDomain(r.Context(), ns, &domain, "update", Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("domain updated: %s (ns=%s), version=%d", name, ns, ver)
	JSON(w, http.StatusOK, map[string]any{"version": ver, "domain": domain})
}

func (h *DomainHandler) DeleteDomain(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")

	ver, err := h.store.DeleteDomain(r.Context(), ns, name, Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("domain deleted: %s (ns=%s), version=%d", name, ns, ver)
	JSON(w, http.StatusOK, map[string]any{"version": ver})
}

// Per-domain history & rollback
func (h *DomainHandler) ListDomainHistory(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")
	history, err := h.store.GetDomainHistory(r.Context(), ns, name)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"history": history, "total": len(history)})
}

func (h *DomainHandler) GetDomainVersion(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")
	versionStr := r.PathValue("version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid version: %v", err))
		return
	}

	entry, err := h.store.GetDomainVersion(r.Context(), ns, name, version)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		ErrJSON(w, http.StatusNotFound, fmt.Sprintf("domain %q version %d not found", name, version))
		return
	}

	JSON(w, http.StatusOK, entry)
}

func (h *DomainHandler) RollbackDomain(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	name := r.PathValue("name")
	versionStr := r.PathValue("version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid version: %v", err))
		return
	}

	newVersion, err := h.store.RollbackDomain(r.Context(), ns, name, version, Operator(r))
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("domain %s (ns=%s) rollback to version %d, new version=%d", name, ns, version, newVersion)
	JSON(w, http.StatusOK, map[string]any{
		"name":           name,
		"rolled_back_to": version,
		"new_version":    newVersion,
	})
}
