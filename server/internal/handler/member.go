package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// MemberHandler handles namespace member management and user admin APIs.
type MemberHandler struct {
	store  store.Store
	logger *zap.SugaredLogger
}

func NewMemberHandler(s store.Store, logger *zap.SugaredLogger) *MemberHandler {
	return &MemberHandler{store: s, logger: logger}
}

// Namespace Members
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

// Group Bindings
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

// Users (admin-only)
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

// ForcePasswordChange sets must_change_password flag for a builtin user (admin only).
// The targeted user will be required to change their password on next login.
func (h *MemberHandler) ForcePasswordChange(w http.ResponseWriter, r *http.Request) {
	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	var req struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.store.SetMustChangePassword(r.Context(), userSub, req.MustChangePassword); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "clear_force_password_change"
	if req.MustChangePassword {
		action = "force_password_change"
	}
	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", userSub, action, Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// CreateBuiltinUser creates a new builtin (email/password) user (admin only).
// Only valid when auth_mode is "builtin".
func (h *MemberHandler) CreateBuiltinUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		ErrJSON(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if len(req.Password) < 6 {
		ErrJSON(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}

	sub := "builtin:" + req.Email
	existing, err := h.store.GetUser(r.Context(), sub)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil {
		ErrJSON(w, http.StatusConflict, "user already exists")
		return
	}

	username := req.Name
	if username == "" {
		username = strings.Split(req.Email, "@")[0]
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, "password hash failed")
		return
	}

	user := &store.User{
		Sub:                sub,
		Username:           strings.Split(req.Email, "@")[0],
		Email:              req.Email,
		Name:               username,
		IsAdmin:            req.IsAdmin,
		MustChangePassword: true,
	}
	if err := h.store.UpsertUser(r.Context(), user); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), sub, string(hash)); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", sub, "create_builtin_user", Operator(r))
	JSON(w, http.StatusCreated, map[string]any{"sub": sub, "email": req.Email})
}

// DeleteUser removes a user (admin only).
func (h *MemberHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	// Prevent deleting yourself.
	id := IdentityFromContext(r.Context())
	if id != nil && id.Subject == userSub {
		ErrJSON(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	if err := h.store.DeleteUser(r.Context(), userSub); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", userSub, "delete_user", Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ResetUserPassword resets a builtin user's password (admin only).
// The user will be flagged with must_change_password = true.
func (h *MemberHandler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	// Only builtin users have passwords.
	if !strings.HasPrefix(userSub, "builtin:") {
		ErrJSON(w, http.StatusBadRequest, "can only reset password for builtin users")
		return
	}

	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.NewPassword == "" {
		ErrJSON(w, http.StatusBadRequest, "new_password is required")
		return
	}
	if len(req.NewPassword) < 6 {
		ErrJSON(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}

	// Verify user exists.
	user, err := h.store.GetUser(r.Context(), userSub)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		ErrJSON(w, http.StatusNotFound, "user not found")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), userSub, string(hash)); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Force the user to change password on next login.
	_ = h.store.SetMustChangePassword(r.Context(), userSub, true)

	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", userSub, "admin_reset_password", Operator(r))
	JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// UpdateUser updates a builtin user's profile (email, name, is_admin) (admin only).
func (h *MemberHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	userSub := r.PathValue("sub")
	if userSub == "" {
		ErrJSON(w, http.StatusBadRequest, "user sub is required")
		return
	}

	var req struct {
		Email   *string `json:"email"`
		Name    *string `json:"name"`
		IsAdmin *bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	user, err := h.store.GetUser(r.Context(), userSub)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		ErrJSON(w, http.StatusNotFound, "user not found")
		return
	}

	if req.Email != nil {
		user.Email = strings.TrimSpace(strings.ToLower(*req.Email))
	}
	if req.Name != nil {
		user.Name = strings.TrimSpace(*req.Name)
	}
	if req.IsAdmin != nil {
		user.IsAdmin = *req.IsAdmin
	}

	if err := h.store.UpsertUser(r.Context(), user); err != nil {
		ErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If is_admin changed, also update via SetUserAdmin to ensure consistency.
	if req.IsAdmin != nil {
		_ = h.store.SetUserAdmin(r.Context(), userSub, *req.IsAdmin)
	}

	_ = h.store.InsertAuditLog(r.Context(), "_global", "user", userSub, "update_user", Operator(r))
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
