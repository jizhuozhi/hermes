package handler

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/config"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// ─── OIDC Auth Handler (login / callback / userinfo / config) ─────────

// oidcEndpoints holds the discovered OIDC provider endpoints.
type oidcEndpoints struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JwksURI               string `json:"jwks_uri"`
}

// OIDCHandler handles the server-side OIDC authorization code flow.
// All provider endpoint URLs are discovered via OIDC Discovery — no
// provider-specific (Keycloak, Okta, etc.) URL patterns are assumed.
type OIDCHandler struct {
	cfg               config.OIDCConfig
	store             store.Store
	logger            *zap.SugaredLogger
	initialAdminUsers map[string]bool // pre-parsed from cfg.InitialAdminUsers (lowercased)
	endpoints         oidcEndpoints   // discovered from .well-known/openid-configuration
}

// discoverOIDCEndpoints fetches {issuer}/.well-known/openid-configuration.
func discoverOIDCEndpoints(issuer string) (*oidcEndpoints, error) {
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OIDC discovery HTTP %d: %s", resp.StatusCode, string(body))
	}
	var ep oidcEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&ep); err != nil {
		return nil, fmt.Errorf("OIDC discovery decode: %w", err)
	}
	if ep.AuthorizationEndpoint == "" || ep.TokenEndpoint == "" || ep.JwksURI == "" {
		return nil, fmt.Errorf("OIDC discovery: missing required endpoints (auth=%q, token=%q, jwks=%q)",
			ep.AuthorizationEndpoint, ep.TokenEndpoint, ep.JwksURI)
	}
	return &ep, nil
}

func NewOIDCHandler(cfg config.OIDCConfig, s store.Store, logger *zap.SugaredLogger) (*OIDCHandler, error) {
	initialAdmins := make(map[string]bool)
	for _, u := range strings.Split(cfg.InitialAdminUsers, ",") {
		u = strings.TrimSpace(strings.ToLower(u))
		if u != "" {
			initialAdmins[u] = true
		}
	}

	ep, err := discoverOIDCEndpoints(cfg.Issuer)
	if err != nil {
		// Fallback: derive endpoints from issuer using common OIDC patterns.
		logger.Warnf("OIDC discovery failed (%v), falling back to standard endpoint derivation from issuer", err)
		base := strings.TrimRight(cfg.Issuer, "/")
		ep = &oidcEndpoints{
			AuthorizationEndpoint: base + "/protocol/openid-connect/auth",
			TokenEndpoint:         base + "/protocol/openid-connect/token",
			JwksURI:               base + "/protocol/openid-connect/certs",
		}
	}
	logger.Infof("OIDC endpoints: auth=%s, token=%s, jwks=%s",
		ep.AuthorizationEndpoint, ep.TokenEndpoint, ep.JwksURI)

	if len(initialAdmins) > 0 {
		logger.Infof("OIDC initial admin users (seed on first login): %v", initialAdmins)
	}

	return &OIDCHandler{
		cfg:               cfg,
		store:             s,
		logger:            logger,
		initialAdminUsers: initialAdmins,
		endpoints:         *ep,
	}, nil
}

// JwksURI returns the discovered JWKS endpoint URL.
func (h *OIDCHandler) JwksURI() string {
	return h.endpoints.JwksURI
}

// Login redirects the user to the OIDC provider's authorization endpoint.
func (h *OIDCHandler) Login(w http.ResponseWriter, r *http.Request) {
	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	} else if r.TLS == nil {
		scheme = "http"
	}
	redirectURI := scheme + "://" + r.Host + "/auth/callback"

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {h.cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"scope":         {"openid profile email"},
	}
	http.Redirect(w, r, h.endpoints.AuthorizationEndpoint+"?"+params.Encode(), http.StatusFound)
}

// Callback handles the OIDC provider redirect: exchanges the authorization code for tokens.
func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		ErrJSON(w, http.StatusBadRequest, "code is required")
		return
	}

	// Reconstruct redirect_uri: must match exactly what was sent in Login.
	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	} else if r.TLS == nil {
		scheme = "http"
	}
	redirectURI := scheme + "://" + r.Host + "/auth/callback"

	resp, err := http.PostForm(h.endpoints.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {h.cfg.ClientID},
		"client_secret": {h.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	})
	if err != nil {
		h.logger.Errorf("OIDC token exchange failed: %v", err)
		ErrJSON(w, http.StatusBadGateway, "token exchange failed")
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize+1))
	if resp.StatusCode != http.StatusOK {
		h.logger.Errorf("OIDC token exchange HTTP %d: %s", resp.StatusCode, string(body))
		ErrJSON(w, http.StatusBadGateway, "token exchange failed: "+string(body))
		return
	}

	var tokenResp map[string]any
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		ErrJSON(w, http.StatusInternalServerError, "invalid token response")
		return
	}

	// Sync user to database on successful login.
	if accessToken, ok := tokenResp["access_token"].(string); ok {
		h.syncUser(r.Context(), accessToken)
	}

	JSON(w, http.StatusOK, tokenResp)
}

// syncUser parses the access token and upserts the user in the database.
// On first login (INSERT), if the user matches initial_admin_users, they get is_admin=true.
// On subsequent logins (UPDATE), is_admin is never changed — fully managed via the UI.
func (h *OIDCHandler) syncUser(ctx context.Context, tokenStr string) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) < 2 {
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var claims struct {
		Sub               string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return
	}
	if claims.Sub == "" {
		return
	}

	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}

	// Determine initial admin status for first-time users only.
	// Match against username and email (case-insensitive).
	isAdmin := false
	if len(h.initialAdminUsers) > 0 {
		if h.initialAdminUsers[strings.ToLower(username)] || h.initialAdminUsers[strings.ToLower(claims.Email)] {
			isAdmin = true
		}
	}

	user := &store.User{
		Sub:      claims.Sub,
		Username: username,
		Email:    claims.Email,
		Name:     claims.Name,
		IsAdmin:  isAdmin,
	}
	// UpsertUser: INSERT uses the isAdmin value; UPDATE preserves existing is_admin.
	if err := h.store.UpsertUser(ctx, user); err != nil {
		h.logger.Warnf("failed to sync user %s: %v", claims.Sub, err)
	}
}

// Userinfo returns the parsed claims from the Bearer JWT token.
func (h *OIDCHandler) Userinfo(w http.ResponseWriter, r *http.Request) {
	claims := OIDCClaimsFromContext(r.Context())
	if claims == nil {
		ErrJSON(w, http.StatusUnauthorized, "no valid token")
		return
	}
	JSON(w, http.StatusOK, claims)
}

// Refresh exchanges a refresh_token for a new access_token via the OIDC token endpoint.
// This keeps the provider URL, client_id, and client_secret server-side only.
func (h *OIDCHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize+1))
	if err != nil {
		ErrJSON(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		ErrJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RefreshToken == "" {
		ErrJSON(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	resp, err := http.PostForm(h.endpoints.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {h.cfg.ClientID},
		"client_secret": {h.cfg.ClientSecret},
		"refresh_token": {req.RefreshToken},
	})
	if err != nil {
		h.logger.Errorf("OIDC refresh failed: %v", err)
		ErrJSON(w, http.StatusBadGateway, "refresh failed")
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize+1))
	if resp.StatusCode != http.StatusOK {
		h.logger.Debugf("OIDC refresh HTTP %d: %s", resp.StatusCode, string(respBody))
		ErrJSON(w, resp.StatusCode, "refresh failed")
		return
	}

	var tokenResp map[string]any
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		ErrJSON(w, http.StatusInternalServerError, "invalid token response")
		return
	}

	// Only return the fields the frontend needs.
	result := map[string]any{
		"access_token": tokenResp["access_token"],
	}
	if rt, ok := tokenResp["refresh_token"]; ok {
		result["refresh_token"] = rt
	}
	JSON(w, http.StatusOK, result)
}

// ─── OIDC JWT Verification (used by Authenticate middleware) ─────────

// OIDCClaims are the standard OIDC claims extracted from a verified JWT.
type OIDCClaims struct {
	Sub              string   `json:"sub"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Email            string   `json:"email,omitempty"`
	Name             string   `json:"name,omitempty"`
	Groups           []string `json:"groups,omitempty"`
	Exp              int64    `json:"exp,omitempty"`
}

// OIDCClaimsFromContext returns OIDC claims from the request context.
// Returns nil if the caller was not authenticated via OIDC.
func OIDCClaimsFromContext(ctx context.Context) *OIDCClaims {
	id := IdentityFromContext(ctx)
	if id == nil {
		return nil
	}
	return id.OIDCClaims
}

// NewOIDCVerifier creates an OIDCVerifyFunc from the OIDC config and JWKS URI.
func NewOIDCVerifier(cfg config.OIDCConfig, jwksURI string) OIDCVerifyFunc {
	cache := newJWKSCache(jwksURI)
	return func(tokenStr string) (*OIDCClaims, error) {
		return verifyJWT(tokenStr, cache, cfg.ClientID)
	}
}

// ─── JWT Verification ────────────────────────────────────────────────

func verifyJWT(tokenStr string, cache *jwksCache, expectedAudience string) (*OIDCClaims, error) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT")
	}

	// Decode header to get kid.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported alg: %s", header.Alg)
	}

	// Get public key from JWKS.
	pubKey, err := cache.getKey(header.Kid)
	if err != nil {
		return nil, fmt.Errorf("get signing key: %w", err)
	}

	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash[:], sigBytes); err != nil {
		return nil, fmt.Errorf("signature verification failed")
	}

	// Decode and validate claims.
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var rawClaims struct {
		OIDCClaims
		Aud json.RawMessage `json:"aud"`
		Azp string          `json:"azp"`
		Iss string          `json:"iss"`
	}
	if err := json.Unmarshal(claimsBytes, &rawClaims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	// Check expiry.
	if rawClaims.Exp > 0 && time.Now().Unix() > rawClaims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	// Check audience: aud can be a string or array of strings.
	// Also accept azp (authorized party) as a match, common in Keycloak.
	audOK := false
	if rawClaims.Azp == expectedAudience {
		audOK = true
	}
	if !audOK {
		var audStr string
		if json.Unmarshal(rawClaims.Aud, &audStr) == nil {
			audOK = audStr == expectedAudience
		}
		if !audOK {
			var audArr []string
			if json.Unmarshal(rawClaims.Aud, &audArr) == nil {
				for _, a := range audArr {
					if a == expectedAudience {
						audOK = true
						break
					}
				}
			}
		}
	}
	if !audOK {
		return nil, fmt.Errorf("audience mismatch")
	}

	return &rawClaims.OIDCClaims, nil
}

// ─── JWKS Cache ──────────────────────────────────────────────────────

type jwksCache struct {
	url     string
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
	sf      singleflight.Group
}

func newJWKSCache(url string) *jwksCache {
	return &jwksCache{url: url, keys: make(map[string]*rsa.PublicKey)}
}

func (c *jwksCache) getKey(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	age := time.Since(c.fetched)
	c.mu.RUnlock()

	if ok && age < 5*time.Minute {
		return key, nil
	}

	// Use singleflight to coalesce concurrent refresh calls.
	_, err, _ := c.sf.Do("refresh", func() (any, error) {
		return nil, c.refresh()
	})
	if err != nil {
		if ok {
			return key, nil
		}
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok = c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}
	return key, nil
}

func (c *jwksCache) refresh() error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(c.url)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS HTTP %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	newKeys := make(map[string]*rsa.PublicKey)
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		n := new(big.Int).SetBytes(nBytes)
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		newKeys[k.Kid] = &rsa.PublicKey{N: n, E: e}
	}

	c.mu.Lock()
	c.keys = newKeys
	c.fetched = time.Now()
	c.mu.Unlock()
	return nil
}
