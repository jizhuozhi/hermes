package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

// Context keys
// Uses unexported struct types as context keys to guarantee uniqueness
// across packages — no risk of collision with int-based keys.

type identityKeyType struct{}
type namespaceKeyType struct{}

var (
	identityKey  = identityKeyType{}
	namespaceKey = namespaceKeyType{}
)

// Identity: unified caller identity (OIDC user or HMAC credential)

// Identity is the unified representation of "who is calling".
// Populated by the Authenticate middleware regardless of auth method.
type Identity struct {
	// Subject identifies the caller: OIDC sub or credential access_key.
	Subject string
	// Namespace the caller is operating in.
	Namespace string
	// Scopes the caller is authorized for.
	Scopes []string
	// Source distinguishes auth method: "oidc" or "hmac".
	Source string
	// OIDCClaims is non-nil only for OIDC-authenticated users.
	OIDCClaims *OIDCClaims
	// Credential is non-nil only for HMAC-authenticated callers.
	Credential *store.APICredential
}

// HasScope returns true if the identity has the given scope.
func (id *Identity) HasScope(scope string) bool {
	for _, s := range id.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// IdentityFromContext returns the authenticated Identity from the request context.
func IdentityFromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// NamespaceFromContext returns the namespace from the request context.
func NamespaceFromContext(ctx context.Context) string {
	ns, _ := ctx.Value(namespaceKey).(string)
	if ns == "" {
		return store.DefaultNamespace
	}
	return ns
}

// Namespace Middleware
// NamespaceMiddleware extracts the namespace from the X-Hermes-Namespace header
// (or ?namespace= query param for web UI) and injects it into context.
func NamespaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns := r.Header.Get("X-Hermes-Namespace")
		if ns == "" {
			ns = r.URL.Query().Get("namespace")
		}
		if ns == "" {
			ns = store.DefaultNamespace
		}
		ctx := context.WithValue(r.Context(), namespaceKey, ns)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Unified Authenticate Middleware
//
// Authenticate inspects the Authorization header and resolves a unified Identity:
//   - "Bearer <jwt>"       → OIDC path: verify JWT, resolve role→scopes
//   - "HMAC-SHA256 ..."    → HMAC path: verify signature, use credential scopes
//   - missing header       → 401 (unless HMAC bootstrap: no credentials in DB yet)

const maxTimestampSkew = 5 * time.Minute

// Authenticate returns a middleware that resolves the caller's Identity.
// It supports both OIDC Bearer tokens and HMAC-SHA256 signatures.
func Authenticate(s store.Store, oidcVerifier OIDCVerifyFunc, logger *zap.SugaredLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			ns := NamespaceFromContext(r.Context())

			switch {
			case strings.HasPrefix(authHeader, "Bearer "):
				// OIDC Bearer token
				identity, err := authenticateOIDC(r.Context(), s, oidcVerifier, authHeader, ns)
				if err != nil {
					logger.Debugf("OIDC auth failed: %v", err)
					ErrJSON(w, http.StatusUnauthorized, err.Error())
					return
				}
				ctx := context.WithValue(r.Context(), identityKey, identity)
				next.ServeHTTP(w, r.WithContext(ctx))

			case strings.HasPrefix(authHeader, "HMAC-SHA256 "):
				// HMAC credential
				identity, err := authenticateHMAC(r, s, logger, ns)
				if err != nil {
					ErrJSON(w, http.StatusUnauthorized, err.Error())
					return
				}
				ctx := context.WithValue(r.Context(), identityKey, identity)
				next.ServeHTTP(w, r.WithContext(ctx))

			case authHeader == "":
				// No auth header. Allow through only for HMAC bootstrap
				// (no credentials exist in DB yet).
				creds, err := s.ListAPICredentials(r.Context(), ns)
				if err != nil {
					logger.Errorf("auth: list credentials: %v", err)
					ErrJSON(w, http.StatusInternalServerError, "auth check failed")
					return
				}
				if len(creds) > 0 {
					ErrJSON(w, http.StatusUnauthorized, "authentication required")
					return
				}
				// Bootstrap mode: no credentials, no identity, allow through.
				next.ServeHTTP(w, r)

			default:
				ErrJSON(w, http.StatusUnauthorized, "unsupported authorization scheme")
			}
		})
	}
}

// OIDCVerifyFunc verifies a Bearer JWT and returns claims.
// This is injected by the OIDCAuth setup so the middleware doesn't depend on config.
type OIDCVerifyFunc func(tokenStr string) (*OIDCClaims, error)

func authenticateOIDC(ctx context.Context, s store.Store, verify OIDCVerifyFunc, authHeader, ns string) (*Identity, error) {
	if verify == nil {
		return nil, fmt.Errorf("OIDC authentication not configured")
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := verify(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	// Resolve role → scopes.
	isAdmin := false
	user, err := s.GetUser(ctx, claims.Sub)
	if err == nil && user != nil {
		isAdmin = user.IsAdmin
	}

	var role store.NamespaceRole
	if !isAdmin {
		role = store.NamespaceRole(resolveEffectiveRole(ctx, s, ns, claims))
	}

	scopes := store.RoleToScopes(role, isAdmin)

	return &Identity{
		Subject:    claims.Sub,
		Namespace:  ns,
		Scopes:     scopes,
		Source:     "oidc",
		OIDCClaims: claims,
	}, nil
}

func authenticateHMAC(r *http.Request, s store.Store, logger *zap.SugaredLogger, ns string) (*Identity, error) {
	authHeader := r.Header.Get("Authorization")

	ak, sig, err := parseHMACAuthHeader(authHeader)
	if err != nil {
		return nil, err
	}

	cred, err := s.GetAPICredentialByAK(r.Context(), ak)
	if err != nil {
		logger.Errorf("HMAC auth: lookup ak=%s: %v", ak, err)
		return nil, fmt.Errorf("auth lookup failed")
	}
	if cred == nil {
		return nil, fmt.Errorf("invalid access key")
	}
	if !cred.Enabled {
		return nil, fmt.Errorf("credential is disabled")
	}

	// Validate timestamp.
	tsStr := r.Header.Get("X-Hermes-Timestamp")
	if tsStr == "" {
		return nil, fmt.Errorf("missing X-Hermes-Timestamp header")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid X-Hermes-Timestamp")
	}
	skew := time.Duration(math.Abs(float64(time.Now().Unix()-ts))) * time.Second
	if skew > maxTimestampSkew {
		return nil, fmt.Errorf("timestamp expired")
	}

	// Read and verify body hash.
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read body failed")
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	bodyHash := sha256Hex(bodyBytes)
	clientBodyHash := r.Header.Get("X-Hermes-Body-SHA256")
	if clientBodyHash != "" && clientBodyHash != bodyHash {
		logger.Warnf("HMAC body hash mismatch: path=%s ak=%s", r.URL.Path, ak)
		return nil, fmt.Errorf("body hash mismatch")
	}

	// Compute expected signature.
	stringToSign := r.Method + "\n" + r.URL.Path + "\n" + tsStr + "\n" + bodyHash
	expected := computeHMACSHA256(cred.SecretKey, stringToSign)

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		logger.Warnf("HMAC signature mismatch: path=%s ak=%s", r.URL.Path, ak)
		return nil, fmt.Errorf("invalid signature")
	}

	return &Identity{
		Subject:    "credential:" + cred.AccessKey,
		Namespace:  cred.Namespace,
		Scopes:     cred.Scopes,
		Source:     "hmac",
		Credential: cred,
	}, nil
}

// resolveEffectiveRole returns the highest role for the user in the given namespace,
// considering both direct membership and group bindings.
func resolveEffectiveRole(ctx context.Context, s store.Store, ns string, claims *OIDCClaims) string {
	var role string

	member, _ := s.GetNamespaceMember(ctx, ns, claims.Sub)
	if member != nil {
		role = string(member.Role)
	}

	if len(claims.Groups) > 0 {
		groupRole, err := s.GetEffectiveRoleByGroups(ctx, ns, claims.Groups)
		if err == nil && groupRole != nil {
			if role == "" || store.RolePriority(*groupRole) > store.RolePriority(store.NamespaceRole(role)) {
				role = string(*groupRole)
			}
		}
	}

	return role
}

// Scope-based Authorization
// RequireScope returns a middleware that checks the caller has the given scope.
// Must be applied AFTER Authenticate + NamespaceMiddleware.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := IdentityFromContext(r.Context())
			if id == nil {
				// No identity = bootstrap mode, allow through.
				next.ServeHTTP(w, r)
				return
			}
			if !id.HasScope(scope) {
				ErrJSON(w, http.StatusForbidden, fmt.Sprintf("scope %q required", scope))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Global Middleware
// CORS wraps a handler with permissive CORS headers.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Hermes-Timestamp, X-Hermes-Body-SHA256, X-Hermes-Namespace")
		w.Header().Set("Access-Control-Max-Age", "43200")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Recovery catches panics and returns a 500 response.
func Recovery(logger *zap.SugaredLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.Errorf("panic recovered: %v\n%s", err, debug.Stack())
				ErrJSON(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Helpers
// Wrap applies a chain of middleware wrappers to a handler.
func Wrap(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// WrapFunc is like Wrap but accepts an http.HandlerFunc.
func WrapFunc(fn http.HandlerFunc, mws ...func(http.Handler) http.Handler) http.Handler {
	return Wrap(fn, mws...)
}

func parseHMACAuthHeader(header string) (accessKey, signature string, err error) {
	if !strings.HasPrefix(header, "HMAC-SHA256 ") {
		return "", "", fmt.Errorf("unsupported auth scheme, expected HMAC-SHA256")
	}
	params := header[len("HMAC-SHA256 "):]

	for _, part := range strings.Split(params, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "Credential":
			accessKey = kv[1]
		case "Signature":
			signature = kv[1]
		}
	}

	if accessKey == "" || signature == "" {
		return "", "", fmt.Errorf("malformed HMAC-SHA256 Authorization header")
	}
	return accessKey, signature, nil
}

func computeHMACSHA256(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
