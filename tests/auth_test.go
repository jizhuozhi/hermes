package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════
//  Server Auth, Audit & RBAC Tests
//
//  These tests exercise the server's authentication, authorization,
//  audit logging, member/group management, namespace management, and
//  user admin capabilities. All tests are pure black-box: the server
//  runs as a compiled binary, and tests interact only via HTTP.
// ══════════════════════════════════════════════════════════════════════

// Bootstrap Mode
// TestE2E_Auth_BootstrapMode verifies that when no credentials exist,
// unauthenticated requests are allowed (bootstrap mode).
func TestE2E_Auth_BootstrapMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiGet(t, base, "/api/v1/domains")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = apiGet(t, base, "/api/v1/config")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = apiGet(t, base, "/api/v1/config/revision")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// Credential Lifecycle
// TestE2E_Auth_CredentialLifecycle tests bootstrap create → HMAC CRUD → delete → revoke.
func TestE2E_Auth_CredentialLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	// Bootstrap: create first credential
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin credential",
		"scopes":      []string{"config:read", "config:write", "credential:read", "credential:write", "audit:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)
	credID := cred["id"].(float64)
	assert.NotEmpty(t, ak)
	assert.NotEmpty(t, sk)

	// Unauthenticated → 401
	resp = apiGet(t, base, "/api/v1/domains")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// HMAC → 200
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create domain via HMAC
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", ak, sk, domainConfig{
		Name: "authed-domain", Hosts: []string{"authed.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "GET", base+"/api/v1/domains/authed-domain", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "authed-domain", data["domain"].(map[string]any)["name"])

	// List credentials
	resp = hmacRequest(t, "GET", base+"/api/v1/credentials", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data = readJSON(t, resp)
	credentials := data["credentials"].([]any)
	assert.GreaterOrEqual(t, len(credentials), 1)

	// Update credential
	resp = hmacRequest(t, "PUT", fmt.Sprintf("%s/api/v1/credentials/%d", base, int64(credID)), ak, sk, map[string]any{
		"description": "updated admin",
		"scopes":      []string{"config:read", "config:write", "credential:read", "credential:write", "audit:read"},
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create + delete second credential
	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", ak, sk, map[string]any{
		"description": "read-only", "scopes": []string{"config:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred2 := readJSON(t, resp)
	ak2 := cred2["access_key"].(string)
	sk2 := cred2["secret_key"].(string)
	cred2ID := cred2["id"].(float64)

	resp = hmacRequest(t, "DELETE", fmt.Sprintf("%s/api/v1/credentials/%d", base, int64(cred2ID)), ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Deleted credential → 401
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak2, sk2, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Scope Enforcement
// TestE2E_Auth_ScopeEnforcement verifies that credentials are scope-gated.
func TestE2E_Auth_ScopeEnforcement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	// Read-only credential
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "reader", "scopes": []string{"config:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Read → OK
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "GET", base+"/api/v1/config", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Write → 403
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", ak, sk, domainConfig{
		Name: "forbidden", Hosts: []string{"forbidden.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Credential management → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/credentials", ak, sk, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Audit → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/audit", ak, sk, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Members → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/members", ak, sk, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Status → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/status", ak, sk, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// TestE2E_Auth_ScopeIsolation verifies fine-grained scope combinations.
func TestE2E_Auth_ScopeIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	// Controller-like credential
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "controller",
		"scopes":      []string{"config:read", "config:watch", "status:write", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ctrlAK := cred["access_key"].(string)
	ctrlSK := cred["secret_key"].(string)

	// Can read config
	resp = hmacRequest(t, "GET", base+"/api/v1/config", ctrlAK, ctrlSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Can watch config
	resp = hmacRequest(t, "GET", base+"/api/v1/config/watch?revision=0", ctrlAK, ctrlSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Can write status
	resp = hmacRequest(t, "PUT", base+"/api/v1/status/controller", ctrlAK, ctrlSK, map[string]any{
		"id": "ctrl-e2e", "status": "running",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// CANNOT write config (no config:write)
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", ctrlAK, ctrlSK, domainConfig{
		Name: "x", Hosts: []string{"x.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// CANNOT read status (no status:read)
	resp = hmacRequest(t, "GET", base+"/api/v1/status", ctrlAK, ctrlSK, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Create status-reader credential
	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", ctrlAK, ctrlSK, map[string]any{
		"description": "status-reader", "scopes": []string{"status:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred2 := readJSON(t, resp)
	statusAK := cred2["access_key"].(string)
	statusSK := cred2["secret_key"].(string)

	// Can read status
	resp = hmacRequest(t, "GET", base+"/api/v1/status", statusAK, statusSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// CANNOT write status
	resp = hmacRequest(t, "PUT", base+"/api/v1/status/controller", statusAK, statusSK, map[string]any{
		"id": "ctrl-x", "status": "running",
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// Signature Verification
// TestE2E_Auth_InvalidSignature verifies invalid HMAC signatures are rejected.
func TestE2E_Auth_InvalidSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "test", "scopes": []string{"config:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)

	// Wrong secret key
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak, "wrong-secret", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// Non-existent access key
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", "non-existent-ak", "some-sk", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Disabled Credential
// TestE2E_Auth_DisabledCredential verifies that disabled credentials are rejected.
func TestE2E_Auth_DisabledCredential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin", "scopes": []string{"config:read", "credential:read", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	adminCred := readJSON(t, resp)
	adminAK := adminCred["access_key"].(string)
	adminSK := adminCred["secret_key"].(string)

	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", adminAK, adminSK, map[string]any{
		"description": "to-disable", "scopes": []string{"config:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred2 := readJSON(t, resp)
	ak2 := cred2["access_key"].(string)
	sk2 := cred2["secret_key"].(string)
	id2 := cred2["id"].(float64)

	// Works initially
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak2, sk2, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Disable
	enabled := false
	resp = hmacRequest(t, "PUT", fmt.Sprintf("%s/api/v1/credentials/%d", base, int64(id2)), adminAK, adminSK, map[string]any{
		"description": "to-disable", "enabled": &enabled, "scopes": []string{"config:read"},
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Disabled → 401
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", ak2, sk2, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// WhoAmI
// TestE2E_Auth_WhoAmI verifies the whoami endpoint.
func TestE2E_Auth_WhoAmI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "whoami-test", "scopes": []string{"config:read", "config:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	resp = hmacRequest(t, "GET", base+"/api/v1/whoami", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "hmac", data["source"])
	assert.Equal(t, "credential:"+ak, data["subject"])
	scopes := data["scopes"].([]any)
	assert.Contains(t, scopes, "config:read")
	assert.Contains(t, scopes, "config:write")
}

// Audit Trail
// TestE2E_Auth_AuditTrail verifies that operations generate audit logs.
func TestE2E_Auth_AuditTrail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "audit-test",
		"scopes":      []string{"config:read", "config:write", "audit:read", "credential:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Perform an action
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", ak, sk, domainConfig{
		Name: "audit-domain", Hosts: []string{"audit.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Check audit log
	resp = hmacRequest(t, "GET", base+"/api/v1/audit", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	total := data["total"].(float64)
	assert.True(t, total > 0, "audit log should have entries")
}

// Namespace-Scoped Credentials
// TestE2E_Auth_NamespaceScopedCredentials verifies credentials are namespace-scoped.
func TestE2E_Auth_NamespaceScopedCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	// Create namespace via API (bootstrap mode)
	resp := apiPost(t, base, "/api/v1/namespaces", map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Default ns credential
	resp = apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "default-cred",
		"scopes":      []string{"config:read", "config:write", "credential:read", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	defaultCred := readJSON(t, resp)
	defaultAK := defaultCred["access_key"].(string)
	defaultSK := defaultCred["secret_key"].(string)

	// Create domain in default
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", defaultAK, defaultSK, domainConfig{
		Name: "default-only", Hosts: []string{"default.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Staging ns credential
	resp = hmacRequestWithNS(t, "POST", base+"/api/v1/credentials", defaultAK, defaultSK, "staging", map[string]any{
		"description": "staging-cred", "scopes": []string{"config:read", "config:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	stagingCred := readJSON(t, resp)
	stagingAK := stagingCred["access_key"].(string)
	stagingSK := stagingCred["secret_key"].(string)

	// Create domain in staging
	resp = hmacRequestWithNS(t, "POST", base+"/api/v1/domains", stagingAK, stagingSK, "staging", domainConfig{
		Name: "staging-only", Hosts: []string{"staging.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Default sees only default
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", defaultAK, defaultSK, nil)
	data := readJSON(t, resp)
	assert.Equal(t, float64(1), data["total"])

	// Staging sees only staging
	resp = hmacRequestWithNS(t, "GET", base+"/api/v1/domains", stagingAK, stagingSK, "staging", nil)
	data = readJSON(t, resp)
	assert.Equal(t, float64(1), data["total"])
}

// Member Management
// TestE2E_Auth_MemberManagement tests adding, listing, updating, and removing members.
// Uses OIDC to create users in the database (code exchange syncs users).
func TestE2E_Auth_MemberManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	base := srv.baseURL

	// Create HMAC credential with member management scopes (bootstrap mode)
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "member-admin",
		"scopes":      []string{"member:read", "member:write", "admin:users"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Create user via OIDC code exchange
	user := &mockOIDCUser{Sub: "user-1", PreferredUsername: "alice", Email: "alice@example.com", Name: "Alice"}
	oidcProvider.RegisterCode("member-mgmt-code", user)
	resp = apiGet(t, base, "/api/auth/token?code=member-mgmt-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Add member
	resp = hmacRequest(t, "POST", base+"/api/v1/members", ak, sk, map[string]any{
		"user_sub": "user-1", "role": "editor",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// List
	resp = hmacRequest(t, "GET", base+"/api/v1/members", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	members := data["members"].([]any)
	assert.Len(t, members, 1)
	m := members[0].(map[string]any)
	assert.Equal(t, "user-1", m["user_sub"])
	assert.Equal(t, "editor", m["role"])

	// Update role
	resp = hmacRequest(t, "POST", base+"/api/v1/members", ak, sk, map[string]any{
		"user_sub": "user-1", "role": "owner",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Remove
	resp = hmacRequest(t, "DELETE", base+"/api/v1/members/user-1", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Verify removed
	resp = hmacRequest(t, "GET", base+"/api/v1/members", ak, sk, nil)
	data = readJSON(t, resp)
	members = data["members"].([]any)
	assert.Len(t, members, 0)
}

// TestE2E_Auth_MemberValidation tests member management edge cases.
func TestE2E_Auth_MemberValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "member-admin", "scopes": []string{"member:read", "member:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Invalid role → 400
	resp = hmacRequest(t, "POST", base+"/api/v1/members", ak, sk, map[string]any{
		"user_sub": "user-1", "role": "superadmin",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Empty user_sub → 400
	resp = hmacRequest(t, "POST", base+"/api/v1/members", ak, sk, map[string]any{
		"user_sub": "", "role": "viewer",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Non-existent user → 404
	resp = hmacRequest(t, "POST", base+"/api/v1/members", ak, sk, map[string]any{
		"user_sub": "non-existent-user", "role": "viewer",
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// Group Bindings
// TestE2E_Auth_GroupBindings tests OIDC group → namespace role bindings.
func TestE2E_Auth_GroupBindings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()
	_ = ctx

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "group-admin", "scopes": []string{"member:read", "member:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Create binding
	resp = hmacRequest(t, "POST", base+"/api/v1/group-bindings", ak, sk, map[string]any{
		"group": "devops", "role": "owner",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// List
	resp = hmacRequest(t, "GET", base+"/api/v1/group-bindings", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	bindings := data["bindings"].([]any)
	assert.Len(t, bindings, 1)
	assert.Equal(t, "devops", bindings[0].(map[string]any)["group"])
	assert.Equal(t, "owner", bindings[0].(map[string]any)["role"])

	// Update
	resp = hmacRequest(t, "POST", base+"/api/v1/group-bindings", ak, sk, map[string]any{
		"group": "devops", "role": "editor",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Add another
	resp = hmacRequest(t, "POST", base+"/api/v1/group-bindings", ak, sk, map[string]any{
		"group": "developers", "role": "viewer",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Delete first
	resp = hmacRequest(t, "DELETE", base+"/api/v1/group-bindings/devops", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "GET", base+"/api/v1/group-bindings", ak, sk, nil)
	data = readJSON(t, resp)
	bindings = data["bindings"].([]any)
	assert.Len(t, bindings, 1)
	assert.Equal(t, "developers", bindings[0].(map[string]any)["group"])
}

// TestE2E_Auth_GroupBindingValidation tests group binding edge cases.
func TestE2E_Auth_GroupBindingValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "test", "scopes": []string{"member:read", "member:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Empty group → 400
	resp = hmacRequest(t, "POST", base+"/api/v1/group-bindings", ak, sk, map[string]any{
		"group": "", "role": "viewer",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Invalid role → 400
	resp = hmacRequest(t, "POST", base+"/api/v1/group-bindings", ak, sk, map[string]any{
		"group": "team", "role": "admin",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// Admin User Management
// TestE2E_Auth_AdminUserManagement tests the admin:users scope for global user management.
// Uses OIDC to create users in the database.
func TestE2E_Auth_AdminUserManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	base := srv.baseURL

	// Create user via OIDC code exchange
	user := &mockOIDCUser{Sub: "user-admin-test", PreferredUsername: "bob", Email: "bob@example.com", Name: "Bob"}
	oidcProvider.RegisterCode("admin-user-mgmt-code", user)
	resp := apiGet(t, base, "/api/auth/token?code=admin-user-mgmt-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create HMAC credentials
	resp = apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin", "scopes": []string{"admin:users", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	adminAK := cred["access_key"].(string)
	adminSK := cred["secret_key"].(string)

	// Non-admin credential
	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", adminAK, adminSK, map[string]any{
		"description": "non-admin", "scopes": []string{"config:read", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred2 := readJSON(t, resp)
	nonAdminAK := cred2["access_key"].(string)
	nonAdminSK := cred2["secret_key"].(string)

	// List users with admin
	resp = hmacRequest(t, "GET", base+"/api/v1/users", adminAK, adminSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	users := data["users"].([]any)
	assert.GreaterOrEqual(t, len(users), 1)

	// List users without admin → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/users", nonAdminAK, nonAdminSK, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Grant admin
	resp = hmacRequest(t, "PUT", base+"/api/v1/users/user-admin-test/admin", adminAK, adminSK, map[string]any{
		"is_admin": true,
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Revoke admin
	resp = hmacRequest(t, "PUT", base+"/api/v1/users/user-admin-test/admin", adminAK, adminSK, map[string]any{
		"is_admin": false,
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Grant without admin scope → 403
	resp = hmacRequest(t, "PUT", base+"/api/v1/users/user-admin-test/admin", nonAdminAK, nonAdminSK, map[string]any{
		"is_admin": true,
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// Namespace Management
// TestE2E_Auth_NamespaceManagement tests namespace CRUD with scope enforcement.
func TestE2E_Auth_NamespaceManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	base := srv.baseURL

	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "ns-admin", "scopes": []string{"namespace:read", "namespace:write", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	ak := cred["access_key"].(string)
	sk := cred["secret_key"].(string)

	// Non-ns credential
	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", ak, sk, map[string]any{
		"description": "no-ns", "scopes": []string{"config:read", "credential:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred2 := readJSON(t, resp)
	noNsAK := cred2["access_key"].(string)
	noNsSK := cred2["secret_key"].(string)

	// List namespaces
	resp = hmacRequest(t, "GET", base+"/api/v1/namespaces", ak, sk, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	nsList := data["namespaces"].([]any)
	assert.Contains(t, nsList, "default")

	// Create namespace
	resp = hmacRequest(t, "POST", base+"/api/v1/namespaces", ak, sk, map[string]any{
		"name": "test-ns",
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Duplicate → 409
	resp = hmacRequest(t, "POST", base+"/api/v1/namespaces", ak, sk, map[string]any{
		"name": "test-ns",
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// Invalid name → 400
	resp = hmacRequest(t, "POST", base+"/api/v1/namespaces", ak, sk, map[string]any{
		"name": "INVALID_NS",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Without scope → 403
	resp = hmacRequest(t, "GET", base+"/api/v1/namespaces", noNsAK, noNsSK, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "POST", base+"/api/v1/namespaces", noNsAK, noNsSK, map[string]any{
		"name": "forbidden-ns",
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// ══════════════════════════════════════════════════════════════════════
//  OIDC E2E Tests
//
//  These tests use a mock OIDC provider (mock RS256 IdP) to exercise
//  the full OIDC authorization code flow, Bearer JWT authentication,
//  token refresh, initial admin seeding, group-binding RBAC, and
//  token validation edge cases.
// ══════════════════════════════════════════════════════════════════════

// OIDC Auth Config
// TestE2E_OIDC_AuthConfigEnabled verifies /api/auth/config reports enabled=true.
func TestE2E_OIDC_AuthConfigEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	resp := apiGet(t, srv.baseURL, "/api/auth/config")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, true, data["enabled"])
}

// Login Redirect
// TestE2E_OIDC_LoginRedirect verifies GET /api/auth/login redirects to the IdP.
func TestE2E_OIDC_LoginRedirect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	// Use a client that doesn't follow redirects.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.baseURL + "/api/auth/login")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, oidcProvider.IssuerURL()+"/authorize")
	assert.Contains(t, loc, "response_type=code")
	assert.Contains(t, loc, "client_id="+oidcProvider.ClientID)
	assert.Contains(t, loc, "scope=openid+profile+email")
}

// Code → Token Exchange
// TestE2E_OIDC_CodeTokenExchange verifies the authorization code → token flow
// and that the user is synced to the database.
func TestE2E_OIDC_CodeTokenExchange(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	base := srv.baseURL

	// Pre-register a code for this user.
	user := &mockOIDCUser{
		Sub: "oidc-user-1", PreferredUsername: "alice",
		Email: "alice@example.com", Name: "Alice Smith",
	}
	oidcProvider.RegisterCode("test-code-1", user)

	// Exchange code for tokens.
	resp := apiGet(t, base, "/api/auth/token?code=test-code-1")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.NotEmpty(t, data["access_token"])
	assert.NotEmpty(t, data["refresh_token"])
	assert.Equal(t, "Bearer", data["token_type"])

	// Invalid code → error.
	resp = apiGet(t, base, "/api/auth/token?code=bad-code")
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	resp.Body.Close()

	// Missing code → 400.
	resp = apiGet(t, base, "/api/auth/token")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// Bearer Token Authentication
// TestE2E_OIDC_BearerAuthentication verifies that a valid JWT grants API access.
func TestE2E_OIDC_BearerAuthentication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "alice",
	})
	defer srv.stop()

	base := srv.baseURL
	user := &mockOIDCUser{
		Sub: "oidc-bearer-1", PreferredUsername: "alice",
		Email: "alice@example.com", Name: "Alice",
	}

	// First, sync the user by doing a code exchange.
	oidcProvider.RegisterCode("bearer-code", user)
	resp := apiGet(t, base, "/api/auth/token?code=bearer-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	tokenData := readJSON(t, resp)
	accessToken := tokenData["access_token"].(string)

	// Use the token to access a protected endpoint.
	resp = bearerRequest(t, "GET", base+"/api/v1/domains", accessToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create a domain using Bearer auth.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", accessToken, domainConfig{
		Name: "oidc-domain", Hosts: []string{"oidc.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Verify domain was created.
	resp = bearerRequest(t, "GET", base+"/api/v1/domains/oidc-domain", accessToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "oidc-domain", data["domain"].(map[string]any)["name"])
}

// Token Refresh
// TestE2E_OIDC_TokenRefresh verifies POST /api/auth/refresh returns a new access token.
func TestE2E_OIDC_TokenRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	base := srv.baseURL
	user := &mockOIDCUser{
		Sub: "oidc-refresh-1", PreferredUsername: "bob",
		Email: "bob@example.com", Name: "Bob",
	}
	oidcProvider.RegisterCode("refresh-code", user)

	// Get initial tokens.
	resp := apiGet(t, base, "/api/auth/token?code=refresh-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	tokenData := readJSON(t, resp)
	refreshToken := tokenData["refresh_token"].(string)

	// Refresh.
	resp = apiPost(t, base, "/api/auth/refresh", map[string]string{
		"refresh_token": refreshToken,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	refreshData := readJSON(t, resp)
	newAccessToken := refreshData["access_token"].(string)
	assert.NotEmpty(t, newAccessToken)

	// Invalid refresh token → error.
	resp = apiPost(t, base, "/api/auth/refresh", map[string]string{
		"refresh_token": "invalid-refresh-token",
	})
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Missing refresh token → 400.
	resp = apiPost(t, base, "/api/auth/refresh", map[string]string{})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// Initial Admin Users
// TestE2E_OIDC_InitialAdminUser verifies that users matching initial_admin_users
// get is_admin=true on first login, and that non-matching users do not.
func TestE2E_OIDC_InitialAdminUser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "admin-alice,admin-bob@example.com",
	})
	defer srv.stop()

	base := srv.baseURL

	// Admin user (matched by username).
	adminUser := &mockOIDCUser{
		Sub: "admin-sub-1", PreferredUsername: "admin-alice",
		Email: "admin-alice@example.com", Name: "Admin Alice",
	}
	oidcProvider.RegisterCode("admin-code", adminUser)
	resp := apiGet(t, base, "/api/auth/token?code=admin-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	adminTokenData := readJSON(t, resp)
	adminToken := adminTokenData["access_token"].(string)

	// Admin should be able to list users (requires admin:users scope).
	resp = bearerRequest(t, "GET", base+"/api/v1/users", adminToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Admin should be able to manage namespaces.
	resp = bearerRequest(t, "GET", base+"/api/v1/namespaces", adminToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Non-admin user.
	normalUser := &mockOIDCUser{
		Sub: "normal-sub-1", PreferredUsername: "charlie",
		Email: "charlie@example.com", Name: "Charlie",
	}
	oidcProvider.RegisterCode("normal-code", normalUser)
	resp = apiGet(t, base, "/api/auth/token?code=normal-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	normalTokenData := readJSON(t, resp)
	normalToken := normalTokenData["access_token"].(string)

	// Non-admin user without any role has no scopes → should be 403 for admin endpoints.
	resp = bearerRequest(t, "GET", base+"/api/v1/users", normalToken, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Admin matched by email.
	emailAdmin := &mockOIDCUser{
		Sub: "admin-sub-2", PreferredUsername: "bob",
		Email: "admin-bob@example.com", Name: "Admin Bob",
	}
	oidcProvider.RegisterCode("email-admin-code", emailAdmin)
	resp = apiGet(t, base, "/api/auth/token?code=email-admin-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	emailAdminTokenData := readJSON(t, resp)
	emailAdminToken := emailAdminTokenData["access_token"].(string)

	resp = bearerRequest(t, "GET", base+"/api/v1/users", emailAdminToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// Userinfo
// TestE2E_OIDC_Userinfo verifies GET /api/auth/userinfo returns correct claims.
func TestE2E_OIDC_Userinfo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	base := srv.baseURL
	user := &mockOIDCUser{
		Sub: "userinfo-sub", PreferredUsername: "diana",
		Email: "diana@example.com", Name: "Diana Prince",
		Groups: []string{"engineering", "devops"},
	}

	token := oidcProvider.signJWT(user, futureExpiry())

	resp := bearerRequest(t, "GET", base+"/api/auth/userinfo", token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "userinfo-sub", data["sub"])
	assert.Equal(t, "diana", data["preferred_username"])
	assert.Equal(t, "diana@example.com", data["email"])
	assert.Equal(t, "Diana Prince", data["name"])
	groups := data["groups"].([]any)
	assert.Contains(t, groups, "engineering")
	assert.Contains(t, groups, "devops")
}

// Group Binding RBAC
// TestE2E_OIDC_GroupBindingRBAC verifies that OIDC groups are resolved to
// namespace roles via group bindings, granting the correct scopes.
func TestE2E_OIDC_GroupBindingRBAC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "setup-admin",
	})
	defer srv.stop()

	base := srv.baseURL

	// Setup: login as admin to create group bindings.
	setupAdmin := &mockOIDCUser{
		Sub: "setup-admin-sub", PreferredUsername: "setup-admin",
		Email: "setup@example.com", Name: "Setup Admin",
	}
	oidcProvider.RegisterCode("setup-code", setupAdmin)
	resp := apiGet(t, base, "/api/auth/token?code=setup-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	adminToken := readJSON(t, resp)["access_token"].(string)

	// Create group binding: "engineering" → editor.
	resp = bearerRequest(t, "POST", base+"/api/v1/group-bindings", adminToken, map[string]any{
		"group": "engineering", "role": "editor",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create group binding: "viewers-only" → viewer.
	resp = bearerRequest(t, "POST", base+"/api/v1/group-bindings", adminToken, map[string]any{
		"group": "viewers-only", "role": "viewer",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// User in "engineering" group → editor scopes.
	engineerUser := &mockOIDCUser{
		Sub: "engineer-1", PreferredUsername: "eng-user",
		Email: "eng@example.com", Name: "Engineer",
		Groups: []string{"engineering"},
	}
	// Sync user first.
	oidcProvider.RegisterCode("eng-code", engineerUser)
	resp = apiGet(t, base, "/api/auth/token?code=eng-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	engToken := readJSON(t, resp)["access_token"].(string)

	// Editor can read config.
	resp = bearerRequest(t, "GET", base+"/api/v1/domains", engToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Editor can write config.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", engToken, domainConfig{
		Name: "eng-domain", Hosts: []string{"eng.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Editor cannot list users (admin:users scope).
	resp = bearerRequest(t, "GET", base+"/api/v1/users", engToken, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// User in "viewers-only" group → viewer scopes.
	viewerUser := &mockOIDCUser{
		Sub: "viewer-1", PreferredUsername: "viewer-user",
		Email: "viewer@example.com", Name: "Viewer",
		Groups: []string{"viewers-only"},
	}
	oidcProvider.RegisterCode("viewer-code", viewerUser)
	resp = apiGet(t, base, "/api/auth/token?code=viewer-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	viewerToken := readJSON(t, resp)["access_token"].(string)

	// Viewer can read.
	resp = bearerRequest(t, "GET", base+"/api/v1/domains", viewerToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Viewer cannot write.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", viewerToken, domainConfig{
		Name: "viewer-domain", Hosts: []string{"viewer.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// User in BOTH groups → gets the highest role (editor > viewer).
	multiGroupUser := &mockOIDCUser{
		Sub: "multi-1", PreferredUsername: "multi-user",
		Email: "multi@example.com", Name: "Multi Group",
		Groups: []string{"viewers-only", "engineering"},
	}
	oidcProvider.RegisterCode("multi-code", multiGroupUser)
	resp = apiGet(t, base, "/api/auth/token?code=multi-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	multiToken := readJSON(t, resp)["access_token"].(string)

	// Should have editor (highest) → can write.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", multiToken, domainConfig{
		Name: "multi-domain", Hosts: []string{"multi.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

// Token Expiry
// TestE2E_OIDC_ExpiredToken verifies that expired JWTs are rejected.
func TestE2E_OIDC_ExpiredToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	user := &mockOIDCUser{
		Sub: "expired-sub", PreferredUsername: "expired-user",
		Email: "expired@example.com", Name: "Expired User",
	}
	expiredToken := oidcProvider.signExpiredJWT(user)

	resp := bearerRequest(t, "GET", srv.baseURL+"/api/v1/domains", expiredToken, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Invalid Signature
// TestE2E_OIDC_InvalidSignature verifies that JWTs signed with wrong key are rejected.
func TestE2E_OIDC_InvalidSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:       pgDSN,
		oidcEnabled: true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID: oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
	})
	defer srv.stop()

	user := &mockOIDCUser{
		Sub: "wrong-sig-sub", PreferredUsername: "wrong-sig",
		Email: "wrongsig@example.com", Name: "Wrong Sig",
	}
	badToken := oidcProvider.signJWTWrongKey(user)

	resp := bearerRequest(t, "GET", srv.baseURL+"/api/v1/domains", badToken, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// WhoAmI OIDC
// TestE2E_OIDC_WhoAmI verifies that /api/v1/whoami reports OIDC source and correct subject.
func TestE2E_OIDC_WhoAmI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "whoami-oidc",
	})
	defer srv.stop()

	base := srv.baseURL
	user := &mockOIDCUser{
		Sub: "whoami-oidc-sub", PreferredUsername: "whoami-oidc",
		Email: "whoami@example.com", Name: "WhoAmI User",
	}

	// Sync user by doing code exchange.
	oidcProvider.RegisterCode("whoami-code", user)
	resp := apiGet(t, base, "/api/auth/token?code=whoami-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token := readJSON(t, resp)["access_token"].(string)

	resp = bearerRequest(t, "GET", base+"/api/v1/whoami", token, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "oidc", data["source"])
	assert.Equal(t, "whoami-oidc-sub", data["sub"])
}

// Direct Member Assignment
// TestE2E_OIDC_DirectMemberRole verifies that a direct member assignment
// (via SetNamespaceMember) gives the user the correct scopes,
// and that direct assignment takes precedence over a lower group binding.
func TestE2E_OIDC_DirectMemberRole(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "direct-admin",
	})
	defer srv.stop()

	base := srv.baseURL

	// Setup: login as admin.
	admin := &mockOIDCUser{
		Sub: "direct-admin-sub", PreferredUsername: "direct-admin",
		Email: "direct-admin@example.com", Name: "Direct Admin",
	}
	oidcProvider.RegisterCode("direct-admin-code", admin)
	resp := apiGet(t, base, "/api/auth/token?code=direct-admin-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	adminToken := readJSON(t, resp)["access_token"].(string)

	// Create group binding: "team" → viewer.
	resp = bearerRequest(t, "POST", base+"/api/v1/group-bindings", adminToken, map[string]any{
		"group": "team", "role": "viewer",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create user and sync.
	targetUser := &mockOIDCUser{
		Sub: "target-user-sub", PreferredUsername: "target-user",
		Email: "target@example.com", Name: "Target User",
		Groups: []string{"team"},
	}
	oidcProvider.RegisterCode("target-code", targetUser)
	resp = apiGet(t, base, "/api/auth/token?code=target-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	targetToken := readJSON(t, resp)["access_token"].(string)

	// With only viewer (from group binding), cannot write.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", targetToken, domainConfig{
		Name: "target-domain", Hosts: []string{"target.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Admin assigns target user as owner directly.
	resp = bearerRequest(t, "POST", base+"/api/v1/members", adminToken, map[string]any{
		"user_sub": "target-user-sub", "role": "owner",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Re-sign token (need fresh token for role re-evaluation).
	targetToken2 := oidcProvider.signJWT(targetUser, futureExpiry())

	// Now with owner (direct > viewer group), can write.
	resp = bearerRequest(t, "POST", base+"/api/v1/domains", targetToken2, domainConfig{
		Name: "target-domain", Hosts: []string{"target.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

// OIDC + HMAC Coexistence
// TestE2E_OIDC_HMACCoexistence verifies that both OIDC Bearer and HMAC-SHA256
// authentication work simultaneously on the same server.
func TestE2E_OIDC_HMACCoexistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	oidcProvider := startMockOIDCProvider(t)
	defer oidcProvider.Close()

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		oidcEnabled:      true, oidcIssuer: oidcProvider.IssuerURL(),
		oidcClientID:     oidcProvider.ClientID, oidcClientSecret: oidcProvider.ClientSecret,
		initialAdminUsers: "coexist-admin",
	})
	defer srv.stop()

	base := srv.baseURL

	// Create HMAC credential (bootstrap mode: no credentials yet).
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "hmac-cred",
		"scopes":      []string{"config:read", "config:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	hmacCred := readJSON(t, resp)
	hmacAK := hmacCred["access_key"].(string)
	hmacSK := hmacCred["secret_key"].(string)

	// HMAC auth works.
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", hmacAK, hmacSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// OIDC auth works alongside.
	admin := &mockOIDCUser{
		Sub: "coexist-admin-sub", PreferredUsername: "coexist-admin",
		Email: "coexist@example.com", Name: "Admin",
	}
	oidcProvider.RegisterCode("coexist-code", admin)
	resp = apiGet(t, base, "/api/auth/token?code=coexist-code")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	oidcToken := readJSON(t, resp)["access_token"].(string)

	resp = bearerRequest(t, "GET", base+"/api/v1/domains", oidcToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create domain via HMAC.
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", hmacAK, hmacSK, domainConfig{
		Name: "hmac-domain", Hosts: []string{"hmac.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Read that domain via OIDC.
	resp = bearerRequest(t, "GET", base+"/api/v1/domains/hmac-domain", oidcToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "hmac-domain", data["domain"].(map[string]any)["name"])

	// Unauthenticated → 401 (credentials exist now, bootstrap mode is off).
	resp = apiGet(t, base, "/api/v1/domains")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// ══════════════════════════════════════════════════════════════════════
//  Builtin Auth E2E Tests
//
//  These tests exercise the built-in username/password authentication
//  system: login, JWT verification, key rotation, and token survival
//  across server restarts.
// ══════════════════════════════════════════════════════════════════════

// Builtin Login
// TestE2E_Builtin_LoginAndAccess verifies the builtin login flow:
// login with email/password → receive JWT → use JWT to access protected endpoints.
func TestE2E_Builtin_LoginAndAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv.stop()

	base := srv.baseURL

	// Auth config should report builtin mode.
	resp := apiGet(t, base, "/api/auth/config")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "builtin", data["mode"])

	// Login with correct credentials.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	tokenData := readJSON(t, resp)
	accessToken := tokenData["access_token"].(string)
	assert.NotEmpty(t, accessToken)

	// Use the token to access a protected endpoint (admin can list users).
	resp = bearerRequest(t, "GET", base+"/api/v1/users", accessToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Login with wrong password → 401.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "wrong",
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// Login with non-existent user → 401.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "nobody@hermes.local", "password": "test",
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Builtin Key Rotation
// TestE2E_Builtin_KeyRotation verifies:
// 1. Tokens issued before rotation remain valid (grace period).
// 2. New tokens are signed with the new key.
// 3. Both old and new tokens work simultaneously during grace period.
func TestE2E_Builtin_KeyRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv.stop()

	base := srv.baseURL

	// Create HMAC credential for admin API access (bootstrap mode).
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin-hmac",
		"scopes":      []string{"config:read", "admin:users"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	cred := readJSON(t, resp)
	hmacAK := cred["access_key"].(string)
	hmacSK := cred["secret_key"].(string)

	// Login and get token (pre-rotation).
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	preRotationToken := readJSON(t, resp)["access_token"].(string)

	// Token works before rotation.
	resp = bearerRequest(t, "GET", base+"/api/v1/users", preRotationToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Rotate the signing key via HMAC-authenticated admin API.
	resp = hmacRequest(t, "POST", base+"/api/auth/rotate-key", hmacAK, hmacSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	rotateData := readJSON(t, resp)
	assert.NotEmpty(t, rotateData["kid"])
	assert.NotEmpty(t, rotateData["grace_period"])

	// Pre-rotation token should STILL work (grace period).
	resp = bearerRequest(t, "GET", base+"/api/v1/users", preRotationToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Login again → get a token signed with the NEW key.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	postRotationToken := readJSON(t, resp)["access_token"].(string)

	// New token works.
	resp = bearerRequest(t, "GET", base+"/api/v1/users", postRotationToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Both tokens should be different (signed with different keys).
	assert.NotEqual(t, preRotationToken, postRotationToken)
}

// Builtin Token Survives Restart
// TestE2E_Builtin_TokenSurvivesRestart verifies that tokens remain valid
// after server restart because the signing key is persisted in PostgreSQL.
func TestE2E_Builtin_TokenSurvivesRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	// Start server and login.
	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})

	resp := apiPost(t, srv.baseURL, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token := readJSON(t, resp)["access_token"].(string)

	// Token works before restart.
	resp = bearerRequest(t, "GET", srv.baseURL+"/api/v1/users", token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Stop and restart the server (same PG, different port).
	srv.stop()
	time.Sleep(500 * time.Millisecond)

	srv2 := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv2.stop()

	// Token from the first instance should still work on the new instance.
	resp = bearerRequest(t, "GET", srv2.baseURL+"/api/v1/users", token, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// Builtin Change Password
// TestE2E_Builtin_ChangePassword verifies the password change flow.
func TestE2E_Builtin_ChangePassword(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv.stop()

	base := srv.baseURL

	// Login with original password.
	resp := apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token := readJSON(t, resp)["access_token"].(string)

	// Change password.
	resp = bearerRequest(t, "POST", base+"/api/auth/change-password", token, map[string]string{
		"old_password": "admin123", "new_password": "newpass456",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Login with old password → 401.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// Login with new password → 200.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "newpass456",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.NotEmpty(t, data["access_token"])
}

// Builtin + HMAC Coexistence
// TestE2E_Builtin_HMACCoexistence verifies that builtin JWT auth and HMAC
// credentials work simultaneously (builtin mode doesn't break HMAC auth).
func TestE2E_Builtin_HMACCoexistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:            pgDSN,
		builtinAuth:      true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv.stop()

	base := srv.baseURL

	// Create HMAC credential (bootstrap mode).
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "hmac-cred",
		"scopes":      []string{"config:read", "config:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	hmacCred := readJSON(t, resp)
	hmacAK := hmacCred["access_key"].(string)
	hmacSK := hmacCred["secret_key"].(string)

	// HMAC auth works.
	resp = hmacRequest(t, "GET", base+"/api/v1/domains", hmacAK, hmacSK, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Builtin JWT auth works alongside.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token := readJSON(t, resp)["access_token"].(string)

	resp = bearerRequest(t, "GET", base+"/api/v1/users", token, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create domain via HMAC, read via builtin JWT.
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", hmacAK, hmacSK, domainConfig{
		Name: "builtin-hmac-domain", Hosts: []string{"bh.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 100}}, Status: 1}},
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = bearerRequest(t, "GET", base+"/api/v1/domains/builtin-hmac-domain", token, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	assert.Equal(t, "builtin-hmac-domain", data["domain"].(map[string]any)["name"])
}

// Builtin User CRUD (Admin)
// TestE2E_Builtin_UserCRUD verifies admin user management in builtin mode:
// create users, update user info, reset password, delete users, and edge cases.
func TestE2E_Builtin_UserCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{
		pgDSN:             pgDSN,
		builtinAuth:       true,
		builtinAdminEmail: "admin@hermes.local",
		builtinAdminPass:  "admin123",
	})
	defer srv.stop()

	base := srv.baseURL

	// Login as admin to get a Bearer token.
	resp := apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "admin@hermes.local", "password": "admin123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	adminToken := readJSON(t, resp)["access_token"].(string)

	// Create a new builtin user
	resp = bearerRequest(t, "POST", base+"/api/v1/users", adminToken, map[string]any{
		"email": "alice@hermes.local", "password": "alice123", "name": "Alice", "is_admin": false,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := readJSON(t, resp)
	aliceSub := created["sub"].(string)
	assert.Equal(t, "builtin:alice@hermes.local", aliceSub)

	// New user can login.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "alice@hermes.local", "password": "alice123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	aliceToken := readJSON(t, resp)["access_token"].(string)
	assert.NotEmpty(t, aliceToken)

	// New user (non-admin) cannot list users.
	resp = bearerRequest(t, "GET", base+"/api/v1/users", aliceToken, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Duplicate create → 409
	resp = bearerRequest(t, "POST", base+"/api/v1/users", adminToken, map[string]any{
		"email": "alice@hermes.local", "password": "alice123",
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// Update user info
	resp = bearerRequest(t, "PUT", base+"/api/v1/users/"+aliceSub, adminToken, map[string]any{
		"name": "Alice Updated", "is_admin": true,
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Verify admin flag: Alice can now list users.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "alice@hermes.local", "password": "alice123",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	aliceToken2 := readJSON(t, resp)["access_token"].(string)

	resp = bearerRequest(t, "GET", base+"/api/v1/users", aliceToken2, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Reset user password
	resp = bearerRequest(t, "PUT", base+"/api/v1/users/"+aliceSub+"/reset-password", adminToken, map[string]any{
		"new_password": "newpass789",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Old password → 401.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "alice@hermes.local", "password": "alice123",
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// New password → 200.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "alice@hermes.local", "password": "newpass789",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Reset password for non-builtin user → 400
	resp = bearerRequest(t, "PUT", base+"/api/v1/users/oidc:someone/reset-password", adminToken, map[string]any{
		"new_password": "whatever",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Reset password for non-existent user → 404
	resp = bearerRequest(t, "PUT", base+"/api/v1/users/builtin:nobody@hermes.local/reset-password", adminToken, map[string]any{
		"new_password": "whatever",
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// Delete self → 400
	adminSub := "builtin:admin@hermes.local"
	resp = bearerRequest(t, "DELETE", base+"/api/v1/users/"+adminSub, adminToken, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Delete user
	resp = bearerRequest(t, "DELETE", base+"/api/v1/users/"+aliceSub, adminToken, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Deleted user cannot login.
	resp = apiPost(t, base, "/api/auth/login", map[string]string{
		"email": "alice@hermes.local", "password": "newpass789",
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// Delete non-existent user → 404
	resp = bearerRequest(t, "DELETE", base+"/api/v1/users/"+aliceSub, adminToken, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// Validation: missing email → 400
	resp = bearerRequest(t, "POST", base+"/api/v1/users", adminToken, map[string]any{
		"email": "", "password": "test123",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Validation: short password → 400
	resp = bearerRequest(t, "POST", base+"/api/v1/users", adminToken, map[string]any{
		"email": "short@hermes.local", "password": "12345",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// futureExpiry returns a time 1 hour in the future, used for JWT expiry.
func futureExpiry() time.Time {
	return time.Now().Add(1 * time.Hour)
}
