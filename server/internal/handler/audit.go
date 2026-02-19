package handler

import (
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type AuditHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewAuditHandler(s store.Store, logger *zap.SugaredLogger) *AuditHandler {
	return &AuditHandler{store: s, logger: logger}
}

func (h *AuditHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}

	entries, total, err := h.store.ListAuditLog(r.Context(), ns, limit, offset)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}
