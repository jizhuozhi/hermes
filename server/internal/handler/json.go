package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// maxRequestBodySize is the maximum allowed request body size (1 MiB).
const maxRequestBodySize = 1 << 20

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Header already sent — can only log, not change status code.
		// The Recovery middleware will catch panics, but Encode errors
		// (broken pipe, etc.) are silently dropped here.
		_ = err
	}
}

// ErrJSON writes an error JSON response: {"error": msg}.
func ErrJSON(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, map[string]string{"error": msg})
}

// ReadBody reads the request body with a size limit to prevent OOM attacks.
// Returns at most maxRequestBodySize bytes.
func ReadBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize+1))
}

// DecodeJSON reads the request body as JSON into v with a size limit.
func DecodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize+1)).Decode(v)
}

// Operator extracts the operator identity from the OIDC claims in context
// (set by OIDCAuth middleware), or falls back to parsing the JWT payload
// directly. Returns empty string if no identity is available.
func Operator(r *http.Request) string {
	// Prefer verified OIDC claims from middleware.
	if claims := OIDCClaimsFromContext(r.Context()); claims != nil {
		if claims.PreferredUsername != "" {
			return claims.PreferredUsername
		}
		if claims.Email != "" {
			return claims.Email
		}
		if claims.Name != "" {
			return claims.Name
		}
		return claims.Sub
	}

	// Fallback: parse JWT payload without verification (for HMAC-authed controller requests
	// that may carry a Bearer token forwarded from the original user).
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == auth {
		return ""
	}
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Name              string `json:"name"`
		Sub               string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	if claims.Email != "" {
		return claims.Email
	}
	if claims.Name != "" {
		return claims.Name
	}
	return claims.Sub
}
