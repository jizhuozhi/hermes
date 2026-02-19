package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

type CredentialHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewCredentialHandler(s store.Store, logger *zap.SugaredLogger) *CredentialHandler {
	return &CredentialHandler{store: s, logger: logger}
}

// ListCredentials returns all API credentials in the current namespace (secret keys are omitted).
func (h *CredentialHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	creds, err := h.store.ListAPICredentials(r.Context(), ns)
	if err != nil {
		h.logger.Errorf("list api credentials: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if creds == nil {
		creds = []store.APICredential{}
	}
	JSON(w, http.StatusOK, map[string]any{"credentials": creds})
}

// CreateCredential generates a new AK/SK pair and stores it in the current namespace.
func (h *CredentialHandler) CreateCredential(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	body, err := ReadBody(r)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Description string   `json:"description"`
		Scopes      []string `json:"scopes"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}

	// Validate scopes.
	for _, s := range req.Scopes {
		if !store.ValidScope(s) {
			ErrJSON(w, http.StatusBadRequest, "invalid scope: "+s)
			return
		}
	}
	if req.Scopes == nil {
		req.Scopes = []string{}
	}

	ak, err := generateRandomHex(16)
	if err != nil {
		h.logger.Errorf("generate access key: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "generate key failed")
		return
	}
	sk, err := generateRandomHex(32)
	if err != nil {
		h.logger.Errorf("generate secret key: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "generate key failed")
		return
	}

	cred := &store.APICredential{
		AccessKey:   ak,
		SecretKey:   sk,
		Description: req.Description,
		Scopes:      req.Scopes,
		Enabled:     true,
	}

	result, err := h.store.CreateAPICredential(r.Context(), ns, cred)
	if err != nil {
		h.logger.Errorf("create api credential: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("api credential created: ns=%s ak=%s desc=%s scopes=%v", ns, result.AccessKey, result.Description, result.Scopes)
	_ = h.store.InsertAuditLog(r.Context(), ns, "credential", result.AccessKey, "create", Operator(r))
	JSON(w, http.StatusCreated, result)
}

// UpdateCredential updates description/enabled of an existing credential.
func (h *CredentialHandler) UpdateCredential(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		ErrJSON(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	body, err := ReadBody(r)
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req struct {
		Description string   `json:"description"`
		Enabled     *bool    `json:"enabled"`
		Scopes      []string `json:"scopes"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}

	// Validate scopes.
	for _, s := range req.Scopes {
		if !store.ValidScope(s) {
			ErrJSON(w, http.StatusBadRequest, "invalid scope: "+s)
			return
		}
	}
	if req.Scopes == nil {
		req.Scopes = []string{}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	cred := &store.APICredential{
		ID:          id,
		Description: req.Description,
		Scopes:      req.Scopes,
		Enabled:     enabled,
	}

	if err := h.store.UpdateAPICredential(r.Context(), ns, cred); err != nil {
		h.logger.Errorf("update api credential: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.store.InsertAuditLog(r.Context(), ns, "credential", idStr, "update", Operator(r))
	JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DeleteCredential deletes an API credential by ID.
func (h *CredentialHandler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		ErrJSON(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	if err := h.store.DeleteAPICredential(r.Context(), ns, id); err != nil {
		h.logger.Errorf("delete api credential: %v", err)
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logger.Infof("api credential deleted: ns=%s id=%d", ns, id)
	_ = h.store.InsertAuditLog(r.Context(), ns, "credential", idStr, "delete", Operator(r))
	JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// generateRandomHex returns a random hex string of n bytes (2n chars).
func generateRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
