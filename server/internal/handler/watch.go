package handler

import (
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type WatchHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewWatchHandler(s store.Store, logger *zap.SugaredLogger) *WatchHandler {
	return &WatchHandler{store: s, logger: logger}
}

// WatchConfig implements long-poll: GET /api/v1/config/watch?revision=N
// Returns changes since revision N. If no changes, blocks up to 30s.
// Region is determined from context (X-Hermes-Region header).
func (h *WatchHandler) WatchConfig(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	sinceStr := r.URL.Query().Get("revision")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			ErrJSON(w, http.StatusBadRequest, "invalid revision")
			return
		}
	}

	events, maxRev, err := h.store.WatchFrom(r.Context(), region, since)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]any{
		"events":   events,
		"revision": maxRev,
		"total":    len(events),
	})
}

// GetRevision returns the current max revision: GET /api/v1/config/revision
func (h *WatchHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
	region := RegionFromContext(r.Context())
	rev, err := h.store.CurrentRevision(r.Context(), region)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"revision": rev})
}
