package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

// MemberHandler handles namespace member management and user admin APIs.
type MemberHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewMemberHandler(s store.Store, logger *zap.SugaredLogger) *MemberHandler {
	return &MemberHandler{store: s, logger: logger}
}

// ── Namespace Members ────────────────────────────

// ListMembers returns all members of a namespace.
func (h *MemberHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	members, err := h.store.ListNamespaceMembers(r.Context(), ns)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if members == nil {
		members = []store.NamespaceMember{}
	}
	JSON(w, http.StatusOK, map[string]any{"members": members})
}

// AddMember adds or updates a member's role in the namespace.
func (h *MemberHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	var req struct {
		UserSub string `json:"user_sub"`
		Role    string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.UserSub = strings.TrimSpace(req.UserSub)
	req.Role = strings.TrimSpace(req.Role)

	if req.UserSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user_sub is required")
		return
	}

	role := store.NamespaceRole(req.Role)
	if role != store.RoleOwner && role != store.RoleEditor && role != store.RoleViewer {
		ErrJSON(w, http.StatusBadRequest, "role must be owner, editor, or viewer")
		return
	}

	// Verify user exists.
	user, err := h.store.GetUser(r.Context(), req.UserSub)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		ErrJSON(w, http.StatusNotFound, "user not found (user must login at least once)")
		return
	}

	if err := h.store.SetNamespaceMember(r.Context(), ns, req.UserSub, role); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.InsertAuditLog(r.Context(), ns, "member", req.UserSub, "set_role:"+req.Role, Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveMember removes a member from the namespace.
func (h *MemberHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	if err := h.store.RemoveNamespaceMember(r.Context(), ns, userSub); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.InsertAuditLog(r.Context(), ns, "member", userSub, "remove", Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Group Bindings ──────────────────────────────

// ListGroupBindings returns all OIDC group → role bindings for a namespace.
func (h *MemberHandler) ListGroupBindings(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	bindings, err := h.store.ListGroupBindings(r.Context(), ns)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bindings == nil {
		bindings = []store.GroupBinding{}
	}
	JSON(w, http.StatusOK, map[string]any{"bindings": bindings})
}

// SetGroupBinding creates or updates an OIDC group → role binding.
func (h *MemberHandler) SetGroupBinding(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	var req struct {
		Group string `json:"group"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Group = strings.TrimSpace(req.Group)
	req.Role = strings.TrimSpace(req.Role)

	if req.Group == "" {
		ErrJSON(w, http.StatusBadRequest, "group is required")
		return
	}

	role := store.NamespaceRole(req.Role)
	if role != store.RoleOwner && role != store.RoleEditor && role != store.RoleViewer {
		ErrJSON(w, http.StatusBadRequest, "role must be owner, editor, or viewer")
		return
	}

	if err := h.store.SetGroupBinding(r.Context(), ns, req.Group, role); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.InsertAuditLog(r.Context(), ns, "group_binding", req.Group, "set_role:"+req.Role, Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveGroupBinding removes an OIDC group binding from the namespace.
func (h *MemberHandler) RemoveGroupBinding(w http.ResponseWriter, r *http.Request) {
	ns := NamespaceFromContext(r.Context())

	group := r.PathValue("group")
	if group == "" {
		ErrJSON(w, http.StatusBadRequest, "group name is required")
		return
	}

	if err := h.store.RemoveGroupBinding(r.Context(), ns, group); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.InsertAuditLog(r.Context(), ns, "group_binding", group, "remove", Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Users (admin-only) ──────────────────────────

// ListUsers returns all known users.
func (h *MemberHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []store.User{}
	}
	JSON(w, http.StatusOK, map[string]any{"users": users})
}

// SetAdmin toggles the admin flag for a user.
func (h *MemberHandler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	var req struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.store.SetUserAdmin(r.Context(), userSub, req.IsAdmin); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "revoke_admin"
	if req.IsAdmin {
		action = "grant_admin"
	}
	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", userSub, action, Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// WhoAmI returns the current caller's identity info.
// For OIDC users: profile + effective role. For credentials: AK + scopes.
func (h *MemberHandler) WhoAmI(w http.ResponseWriter, r *http.Request) {
	id := IdentityFromContext(r.Context())
	if id == nil {
		ErrJSON(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if id.Source == "hmac" {
		JSON(w, http.StatusOK, map[string]any{
			"source":     "hmac",
			"subject":    id.Subject,
			"namespace":  id.Namespace,
			"scopes":     id.Scopes,
			"access_key": id.Credential.AccessKey,
		})
		return
	}

	// OIDC user
	claims := id.OIDCClaims
	if claims == nil {
		ErrJSON(w, http.StatusUnauthorized, "authentication required")
		return
	}

	user, err := h.store.GetUser(r.Context(), claims.Sub)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		ErrJSON(w, http.StatusNotFound, "user not found")
		return
	}

	ns := NamespaceFromContext(r.Context())

	var role string
	var roleSource string
	if user.IsAdmin {
		role = "admin"
		roleSource = "admin"
	} else {
		member, _ := h.store.GetNamespaceMember(r.Context(), ns, claims.Sub)
		if member != nil {
			role = string(member.Role)
			roleSource = "direct"
		}
		if len(claims.Groups) > 0 {
			groupRole, err := h.store.GetEffectiveRoleByGroups(r.Context(), ns, claims.Groups)
			if err == nil && groupRole != nil {
				if role == "" || store.RolePriority(*groupRole) > store.RolePriority(store.NamespaceRole(role)) {
					role = string(*groupRole)
					roleSource = "group"
				}
			}
		}
	}

	JSON(w, http.StatusOK, map[string]any{
		"source":      "oidc",
		"sub":         user.Sub,
		"username":    user.Username,
		"email":       user.Email,
		"name":        user.Name,
		"groups":      claims.Groups,
		"is_admin":    user.IsAdmin,
		"role":        role,
		"role_source": roleSource,
		"scopes":      id.Scopes,
	})
}
