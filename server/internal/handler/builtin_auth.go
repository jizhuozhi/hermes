package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/config"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// Built-in Auth Handler (username/password login + self-signed JWT)
// BuiltinAuthHandler handles username/password authentication without an
// external OIDC provider. It issues self-signed JWTs (HMAC-SHA256) that
// are verified by the same Authenticate middleware.
//
// Signing keys are persisted in PostgreSQL (jwt_signing_keys table) so that:
//   - Tokens survive server restarts
//   - Multiple replicas share the same key
//   - Key rotation is graceful (old keys remain valid during a grace period)
type BuiltinAuthHandler struct {
	store    store.Store
	logger   *zap.SugaredLogger
	tokenTTL time.Duration
}

// NewBuiltinAuthHandler creates a handler for built-in authentication.
// It ensures a signing key exists in the database and seeds the initial
// admin user if configured.
func NewBuiltinAuthHandler(cfg config.BuiltinAuthConfig, s store.Store, logger *zap.SugaredLogger) (*BuiltinAuthHandler, error) {
	h := &BuiltinAuthHandler{
		store:    s,
		logger:   logger,
		tokenTTL: 24 * time.Hour,
	}

	// Ensure a signing key exists in the DB.
	if err := h.ensureSigningKey(); err != nil {
		return nil, fmt.Errorf("ensure signing key: %w", err)
	}

	// Seed initial admin user if configured.
	if cfg.InitialAdminEmail != "" && cfg.InitialAdminPassword != "" {
		if err := h.seedInitialAdmin(cfg.InitialAdminEmail, cfg.InitialAdminPassword); err != nil {
			logger.Warnf("failed to seed initial admin: %v", err)
		}
	}

	return h, nil
}

// ensureSigningKey checks for an active signing key in the DB.
// If none exists, creates one with a securely generated random key.
func (h *BuiltinAuthHandler) ensureSigningKey() error {
	existing, err := h.store.GetActiveSigningKey(nil)
	if err != nil {
		return err
	}
	if existing != nil {
		h.logger.Infof("JWT signing key loaded from DB (kid=%s, created=%s)", existing.KID, existing.CreatedAt.Format(time.RFC3339))
		return nil
	}

	// No active key — create one with a random secret.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate random key: %w", err)
	}
	h.logger.Info("Creating initial JWT signing key (random)")

	kid := generateKeyKID()
	key := &store.JWTSigningKey{
		KID:       kid,
		Secret:    secret,
		Status:    "active",
		CreatedAt: time.Now(),
	}
	if err := h.store.CreateSigningKey(nil, key); err != nil {
		return err
	}
	h.logger.Infof("JWT signing key persisted to DB (kid=%s)", kid)
	return nil
}

func generateKeyKID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("k-%x", b)
}

// seedInitialAdmin creates the initial admin user if it doesn't already exist.
// New initial admin users are flagged with must_change_password = true.
func (h *BuiltinAuthHandler) seedInitialAdmin(email, password string) error {
	sub := "builtin:" + strings.ToLower(email)

	existing, err := h.store.GetUser(nil, sub)
	if err != nil {
		return err
	}
	if existing != nil {
		// User exists, update password hash in case it changed.
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		return h.store.UpdateUserPassword(nil, sub, string(hash))
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	user := &store.User{
		Sub:                sub,
		Username:           strings.Split(email, "@")[0],
		Email:              email,
		Name:               strings.Split(email, "@")[0],
		IsAdmin:            true,
		MustChangePassword: true,
	}
	if err := h.store.UpsertUser(nil, user); err != nil {
		return err
	}
	return h.store.UpdateUserPassword(nil, sub, string(hash))
}

// Login handles POST /api/auth/login with email/password.
func (h *BuiltinAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		ErrJSON(w, http.StatusBadRequest, "email and password are required")
		return
	}

	sub := "builtin:" + req.Email

	// Lookup user and verify password.
	passwordHash, err := h.store.GetUserPasswordHash(r.Context(), sub)
	if err != nil || passwordHash == "" {
		ErrJSON(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		ErrJSON(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	// Get user info.
	user, err := h.store.GetUser(r.Context(), sub)
	if err != nil || user == nil {
		ErrJSON(w, http.StatusInternalServerError, "user lookup failed")
		return
	}

	// Issue JWT.
	accessToken, err := h.issueJWT(r.Context(), user)
	if err != nil {
		h.logger.Errorf("issue JWT: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "token generation failed")
		return
	}

	// Update last_seen.
	_ = h.store.UpsertUser(r.Context(), user)

	resp := map[string]any{
		"access_token": accessToken,
	}
	if user.MustChangePassword {
		resp["must_change_password"] = true
	}
	JSON(w, http.StatusOK, resp)
}

// issueJWT creates an HMAC-SHA256 signed JWT for the given user.
// The active signing key is fetched from the database.
func (h *BuiltinAuthHandler) issueJWT(ctx context.Context, user *store.User) (string, error) {
	// Get the active signing key from DB.
	key, err := h.store.GetActiveSigningKey(ctx)
	if err != nil {
		return "", fmt.Errorf("get signing key: %w", err)
	}
	if key == nil {
		return "", fmt.Errorf("no active signing key in database")
	}

	// Build JWT header with kid.
	headerObj := map[string]string{"alg": "HS256", "typ": "JWT", "kid": key.KID}
	headerJSON, _ := json.Marshal(headerObj)
	header := base64.RawURLEncoding.EncodeToString(headerJSON)

	now := time.Now()
	claims := map[string]any{
		"sub":                user.Sub,
		"preferred_username": user.Username,
		"email":              user.Email,
		"name":               user.Name,
		"iat":                now.Unix(),
		"exp":                now.Add(h.tokenTTL).Unix(),
		"iss":                "hermes-builtin",
		"aud":                "hermes",
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, key.Secret)
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature, nil
}

// NewBuiltinVerifier creates an OIDCVerifyFunc that verifies self-signed HS256 JWTs.
// It looks up the signing key from the database by kid (or falls back to trying all
// valid keys). This allows the existing Authenticate middleware to work seamlessly.
func NewBuiltinVerifier(s store.Store) OIDCVerifyFunc {
	return func(tokenStr string) (*OIDCClaims, error) {
		return verifyBuiltinJWT(tokenStr, s)
	}
}

func verifyBuiltinJWT(tokenStr string, s store.Store) (*OIDCClaims, error) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT")
	}

	// Decode header to extract kid.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	// Try to find the key.
	var keys []store.JWTSigningKey
	if header.KID != "" {
		// Lookup by kid (fast path).
		key, err := s.GetSigningKeyByID(nil, header.KID)
		if err != nil {
			return nil, fmt.Errorf("lookup key: %w", err)
		}
		if key != nil {
			keys = []store.JWTSigningKey{*key}
		}
	}
	if len(keys) == 0 {
		// Fallback: try all valid keys (for tokens issued before kid was added).
		var err error
		keys, err = s.ListValidSigningKeys(nil)
		if err != nil {
			return nil, fmt.Errorf("list keys: %w", err)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid signing keys found")
	}

	// Try each key until one verifies.
	signingInput := parts[0] + "." + parts[1]
	for _, key := range keys {
		mac := hmac.New(sha256.New, key.Secret)
		mac.Write([]byte(signingInput))
		expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

		if hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
			// Signature valid — decode and validate claims.
			claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				return nil, fmt.Errorf("decode claims: %w", err)
			}

			var rawClaims struct {
				OIDCClaims
				Iss string `json:"iss"`
				Aud string `json:"aud"`
			}
			if err := json.Unmarshal(claimsBytes, &rawClaims); err != nil {
				return nil, fmt.Errorf("parse claims: %w", err)
			}

			// Check expiry.
			if rawClaims.Exp > 0 && time.Now().Unix() > rawClaims.Exp {
				return nil, fmt.Errorf("token expired")
			}

			return &rawClaims.OIDCClaims, nil
		}
	}

	return nil, fmt.Errorf("signature verification failed")
}

// RotateKey creates a new signing key and retires the old one.
// The old key stays valid for gracePeriod so in-flight tokens don't break.
func (h *BuiltinAuthHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	newKey, err := h.store.RotateSigningKey(r.Context(), h.tokenTTL)
	if err != nil {
		h.logger.Errorf("rotate signing key: %v", err)
		ErrJSON(w, http.StatusInternalServerError, "key rotation failed")
		return
	}

	h.logger.Infof("JWT signing key rotated: new kid=%s, old keys valid for %s", newKey.KID, h.tokenTTL)
	JSON(w, http.StatusOK, map[string]any{
		"kid":          newKey.KID,
		"grace_period": h.tokenTTL.String(),
	})
}

// Userinfo returns the user profile for the authenticated caller.
func (h *BuiltinAuthHandler) Userinfo(w http.ResponseWriter, r *http.Request) {
	claims := OIDCClaimsFromContext(r.Context())
	if claims == nil {
		ErrJSON(w, http.StatusUnauthorized, "no valid token")
		return
	}
	JSON(w, http.StatusOK, claims)
}

// ChangePassword allows an authenticated user to change their password.
func (h *BuiltinAuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	id := IdentityFromContext(r.Context())
	if id == nil {
		ErrJSON(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := DecodeJSON(r, &req); err != nil {
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

	// Verify old password.
	passwordHash, err := h.store.GetUserPasswordHash(r.Context(), id.Subject)
	if err != nil || passwordHash == "" {
		ErrJSON(w, http.StatusBadRequest, "cannot change password for this account")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.OldPassword)); err != nil {
		ErrJSON(w, http.StatusUnauthorized, "incorrect current password")
		return
	}

	// Hash new password and update.
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		ErrJSON(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), id.Subject, string(newHash)); err != nil {
		ErrJSON(w, http.StatusInternalServerError, "update password failed")
		return
	}

	// Clear the must_change_password flag after a successful password change.
	_ = h.store.SetMustChangePassword(r.Context(), id.Subject, false)

	JSON(w, http.StatusOK, map[string]any{"ok": true})
}
