package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func startPostgres(t *testing.T, ctx context.Context) (*PgStore, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("hermes_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	logger, _ := zap.NewDevelopment()
	store, err := NewPgStore(connStr, logger.Sugar())
	require.NoError(t, err)

	return store, func() {
		store.Close()
		pgContainer.Terminate(ctx)
	}
}

func sampleDomain(name string) *model.DomainConfig {
	return &model.DomainConfig{
		Name:  name,
		Hosts: []string{name + ".example.com"},
		Routes: []model.RouteConfig{
			{
				ID:       "1",
				Name:     "catch-all",
				URI:      "/*",
				Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}},
				Status:   1,
			},
		},
	}
}

func sampleCluster(name string) *model.ClusterConfig {
	return &model.ClusterConfig{
		Name:   name,
		LBType: "roundrobin",
		Timeout: model.TimeoutConfig{
			Connect: 3.0,
			Send:    6.0,
			Read:    6.0,
		},
		Scheme:   "http",
		PassHost: "pass",
		Nodes: []model.UpstreamNode{
			{Host: "10.0.0.1", Port: 8080, Weight: 100},
		},
	}
}

// ── Domain CRUD Tests ──────────────────────────

func TestDomainCRUD(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	ver, err := s.PutDomain(ctx, ns, sampleDomain("api"), "create", "test")
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)

	// Get
	d, err := s.GetDomain(ctx, ns, "api")
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "api", d.Name)
	assert.Equal(t, []string{"api.example.com"}, d.Hosts)

	// List
	domains, err := s.ListDomains(ctx, ns)
	require.NoError(t, err)
	assert.Len(t, domains, 1)

	// Update
	updated := sampleDomain("api")
	updated.Hosts = []string{"api-v2.example.com"}
	ver2, err := s.PutDomain(ctx, ns, updated, "update", "test")
	require.NoError(t, err)
	assert.Equal(t, int64(2), ver2)

	d2, _ := s.GetDomain(ctx, ns, "api")
	assert.Equal(t, []string{"api-v2.example.com"}, d2.Hosts)

	// Delete
	ver3, err := s.DeleteDomain(ctx, ns, "api", "test")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver3)

	d3, err := s.GetDomain(ctx, ns, "api")
	require.NoError(t, err)
	assert.Nil(t, d3)
}

func TestDeleteDomainNotFound(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	_, err := s.DeleteDomain(ctx, "default", "nonexistent", "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── Cluster CRUD Tests ─────────────────────────

func TestClusterCRUD(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	ver, err := s.PutCluster(ctx, ns, sampleCluster("backend"), "create", "test")
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)

	c, err := s.GetCluster(ctx, ns, "backend")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "backend", c.Name)
	assert.Equal(t, "roundrobin", c.LBType)

	clusters, err := s.ListClusters(ctx, ns)
	require.NoError(t, err)
	assert.Len(t, clusters, 1)

	// Delete
	_, err = s.DeleteCluster(ctx, ns, "backend", "test")
	require.NoError(t, err)
	c2, _ := s.GetCluster(ctx, ns, "backend")
	assert.Nil(t, c2)
}

// ── History & Rollback Tests ───────────────────

func TestDomainHistory(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create v1
	d := sampleDomain("hist")
	s.PutDomain(ctx, ns, d, "create", "alice")

	// Update v2
	d.Hosts = []string{"hist-v2.example.com"}
	s.PutDomain(ctx, ns, d, "update", "bob")

	// Get history
	history, err := s.GetDomainHistory(ctx, ns, "hist")
	require.NoError(t, err)
	assert.Len(t, history, 2)
	assert.Equal(t, int64(2), history[0].Version) // newest first
	assert.Equal(t, "update", history[0].Action)
	assert.Equal(t, "bob", history[0].Operator)
	assert.Equal(t, int64(1), history[1].Version)
	assert.Equal(t, "create", history[1].Action)

	// Get specific version
	v1, err := s.GetDomainVersion(ctx, ns, "hist", 1)
	require.NoError(t, err)
	require.NotNil(t, v1)
	assert.Equal(t, "create", v1.Action)

	// Rollback to v1
	ver, err := s.RollbackDomain(ctx, ns, "hist", 1, "charlie")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver) // v3 is the rollback

	d2, _ := s.GetDomain(ctx, ns, "hist")
	assert.Equal(t, "hist.example.com", d2.Hosts[0])
}

func TestClusterHistory(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"
	c := sampleCluster("hist-cluster")
	s.PutCluster(ctx, ns, c, "create", "alice")

	c.LBType = "random"
	s.PutCluster(ctx, ns, c, "update", "bob")

	history, err := s.GetClusterHistory(ctx, ns, "hist-cluster")
	require.NoError(t, err)
	assert.Len(t, history, 2)

	// Rollback
	ver, err := s.RollbackCluster(ctx, ns, "hist-cluster", 1, "charlie")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver)

	c2, _ := s.GetCluster(ctx, ns, "hist-cluster")
	assert.Equal(t, "roundrobin", c2.LBType)
}

// ── Watch & Revision Tests ─────────────────────

func TestWatchFrom(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Initial revision should be 0
	rev, err := s.CurrentRevision(ctx, ns)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rev)

	// Create some data
	s.PutDomain(ctx, ns, sampleDomain("watch1"), "create", "test")
	s.PutCluster(ctx, ns, sampleCluster("watch-c1"), "create", "test")

	// Watch from 0
	events, maxRev, err := s.WatchFrom(ctx, ns, 0)
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.True(t, maxRev > 0)

	// Watch from maxRev should return no events
	events2, _, err := s.WatchFrom(ctx, ns, maxRev)
	require.NoError(t, err)
	assert.Empty(t, events2)

	// One more change
	s.PutDomain(ctx, ns, sampleDomain("watch2"), "create", "test")
	events3, _, err := s.WatchFrom(ctx, ns, maxRev)
	require.NoError(t, err)
	assert.Len(t, events3, 1)
	assert.Equal(t, "domain", events3[0].Kind)
	assert.Equal(t, "watch2", events3[0].Name)
}

// ── Namespace Tests ────────────────────────────

func TestNamespaces(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	// Default namespace should exist
	nsList, err := s.ListNamespaces(ctx)
	require.NoError(t, err)
	assert.Contains(t, nsList, "default")

	// Create new namespace
	err = s.CreateNamespace(ctx, "production")
	require.NoError(t, err)

	nsList, err = s.ListNamespaces(ctx)
	require.NoError(t, err)
	assert.Contains(t, nsList, "production")

	// Namespace isolation: data in one ns doesn't appear in another
	s.PutDomain(ctx, "default", sampleDomain("d1"), "create", "test")
	s.PutDomain(ctx, "production", sampleDomain("d2"), "create", "test")

	defaultDomains, _ := s.ListDomains(ctx, "default")
	prodDomains, _ := s.ListDomains(ctx, "production")
	assert.Len(t, defaultDomains, 1)
	assert.Equal(t, "d1", defaultDomains[0].Name)
	assert.Len(t, prodDomains, 1)
	assert.Equal(t, "d2", prodDomains[0].Name)
}

// ── Bulk Config Tests ──────────────────────────

func TestPutAllConfig(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Pre-populate
	s.PutDomain(ctx, ns, sampleDomain("old"), "create", "test")
	s.PutCluster(ctx, ns, sampleCluster("old-c"), "create", "test")

	// Replace all
	newDomains := []model.DomainConfig{*sampleDomain("new1"), *sampleDomain("new2")}
	newClusters := []model.ClusterConfig{*sampleCluster("new-c")}
	_, err := s.PutAllConfig(ctx, ns, newDomains, newClusters, "import-test")
	require.NoError(t, err)

	// Old data should be gone
	d, _ := s.GetDomain(ctx, ns, "old")
	assert.Nil(t, d)
	c, _ := s.GetCluster(ctx, ns, "old-c")
	assert.Nil(t, c)

	// New data should exist
	domains, _ := s.ListDomains(ctx, ns)
	assert.Len(t, domains, 2)
	clusters, _ := s.ListClusters(ctx, ns)
	assert.Len(t, clusters, 1)
}

// ── Audit Log Tests ────────────────────────────

func TestAuditLog(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	s.PutDomain(ctx, ns, sampleDomain("audit1"), "create", "alice")
	s.PutDomain(ctx, ns, sampleDomain("audit2"), "create", "bob")
	s.DeleteDomain(ctx, ns, "audit1", "charlie")

	entries, total, err := s.ListAuditLog(ctx, ns, 50, 0)
	require.NoError(t, err)
	assert.True(t, total >= 3)
	assert.True(t, len(entries) >= 3)
}

// ── API Credentials Tests ──────────────────────

func TestAPICredentialsCRUD(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	cred := &APICredential{
		AccessKey:   "test-ak-12345",
		SecretKey:   "test-sk-secret",
		Description: "test credential",
		Scopes:      []string{ScopeConfigRead, ScopeConfigWrite},
		Enabled:     true,
	}
	result, err := s.CreateAPICredential(ctx, ns, cred)
	require.NoError(t, err)
	assert.True(t, result.ID > 0)
	assert.Equal(t, ns, result.Namespace)

	// List
	creds, err := s.ListAPICredentials(ctx, ns)
	require.NoError(t, err)
	assert.Len(t, creds, 1)
	assert.Equal(t, "test-ak-12345", creds[0].AccessKey)

	// Get by AK (global lookup)
	found, err := s.GetAPICredentialByAK(ctx, "test-ak-12345")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "test-sk-secret", found.SecretKey)
	assert.True(t, found.Enabled)

	// Update
	found.Description = "updated"
	found.Scopes = []string{ScopeConfigRead}
	err = s.UpdateAPICredential(ctx, ns, found)
	require.NoError(t, err)

	// Delete
	err = s.DeleteAPICredential(ctx, ns, found.ID)
	require.NoError(t, err)

	creds2, _ := s.ListAPICredentials(ctx, ns)
	assert.Empty(t, creds2)
}

func TestGetAPICredentialByAK_NotFound(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	found, err := s.GetAPICredentialByAK(ctx, "nonexistent-ak")
	require.NoError(t, err)
	assert.Nil(t, found)
}

// ── Gateway Status Tests ───────────────────────

func TestGatewayInstanceStatus(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"
	instances := []GatewayInstanceStatus{
		{ID: "gw-1", Status: "running", ConfigRevision: 10},
		{ID: "gw-2", Status: "running", ConfigRevision: 10},
	}

	err := s.UpsertGatewayInstances(ctx, ns, instances)
	require.NoError(t, err)

	list, err := s.ListGatewayInstances(ctx, ns)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// Update: remove one
	err = s.UpsertGatewayInstances(ctx, ns, instances[:1])
	require.NoError(t, err)

	list2, _ := s.ListGatewayInstances(ctx, ns)
	assert.Len(t, list2, 1)
}

func TestControllerStatus(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"
	ctrl := &ControllerStatus{
		ID:              "ctrl-1",
		Status:          "running",
		StartedAt:       time.Now().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().Format(time.RFC3339),
		ConfigRevision:  42,
	}

	err := s.UpsertControllerStatus(ctx, ns, ctrl)
	require.NoError(t, err)

	got, err := s.GetControllerStatus(ctx, ns)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "ctrl-1", got.ID)
	assert.Equal(t, "running", got.Status)
	assert.Equal(t, int64(42), got.ConfigRevision)
}

// ── Grafana Dashboards Tests ───────────────────

func TestGrafanaDashboards(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	d1, err := s.PutGrafanaDashboard(ctx, ns, &GrafanaDashboard{
		Name: "Overview",
		URL:  "https://grafana.example.com/d/abc123",
	})
	require.NoError(t, err)
	assert.True(t, d1.ID > 0)

	// List
	dashboards, err := s.ListGrafanaDashboards(ctx, ns)
	require.NoError(t, err)
	assert.Len(t, dashboards, 1)

	// Update
	d1.Name = "Updated Overview"
	_, err = s.PutGrafanaDashboard(ctx, ns, d1)
	require.NoError(t, err)

	// Delete
	err = s.DeleteGrafanaDashboard(ctx, ns, d1.ID)
	require.NoError(t, err)

	dashboards2, _ := s.ListGrafanaDashboards(ctx, ns)
	assert.Empty(t, dashboards2)
}

// ── Scope / Role Tests ─────────────────────────

func TestValidScope(t *testing.T) {
	assert.True(t, ValidScope(ScopeConfigRead))
	assert.True(t, ValidScope(ScopeAdminUsers))
	assert.False(t, ValidScope("invalid:scope"))
	assert.False(t, ValidScope(""))
}

func TestRoleToScopes(t *testing.T) {
	adminScopes := RoleToScopes(RoleOwner, true)
	assert.Equal(t, AllScopes, adminScopes)

	ownerScopes := RoleToScopes(RoleOwner, false)
	assert.Contains(t, ownerScopes, ScopeConfigWrite)
	assert.Contains(t, ownerScopes, ScopeMemberWrite)

	viewerScopes := RoleToScopes(RoleViewer, false)
	assert.Contains(t, viewerScopes, ScopeConfigRead)
	assert.NotContains(t, viewerScopes, ScopeConfigWrite)
	assert.NotContains(t, viewerScopes, ScopeMemberWrite)

	noRole := RoleToScopes("", false)
	assert.Nil(t, noRole)
}

func TestRolePriority(t *testing.T) {
	assert.True(t, RolePriority(RoleOwner) > RolePriority(RoleEditor))
	assert.True(t, RolePriority(RoleEditor) > RolePriority(RoleViewer))
	assert.True(t, RolePriority(RoleViewer) > 0)
	assert.Equal(t, 0, RolePriority(""))
}

func TestAPICredentialHasScope(t *testing.T) {
	cred := &APICredential{
		Scopes: []string{ScopeConfigRead, ScopeConfigWatch},
	}
	assert.True(t, cred.HasScope(ScopeConfigRead))
	assert.True(t, cred.HasScope(ScopeConfigWatch))
	assert.False(t, cred.HasScope(ScopeConfigWrite))
}

func TestValidateNamespaceName(t *testing.T) {
	assert.Empty(t, ValidateNamespaceName("default"))
	assert.Empty(t, ValidateNamespaceName("production"))
	assert.Empty(t, ValidateNamespaceName("my-namespace-123"))
	assert.Empty(t, ValidateNamespaceName("a"))
	assert.NotEmpty(t, ValidateNamespaceName(""))
	assert.NotEmpty(t, ValidateNamespaceName("-bad"))
	assert.NotEmpty(t, ValidateNamespaceName("bad-"))
	assert.NotEmpty(t, ValidateNamespaceName("UPPERCASE"))
	assert.NotEmpty(t, ValidateNamespaceName("has space"))

	// 63 chars should be ok
	assert.Empty(t, ValidateNamespaceName("a"+strings.Repeat("b", 61)+"c"))
	// 64 chars should fail
	assert.NotEmpty(t, ValidateNamespaceName(strings.Repeat("a", 64)))
}
