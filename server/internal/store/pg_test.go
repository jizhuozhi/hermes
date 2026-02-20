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

// Domain CRUD Tests
func TestDomainCRUD(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	ver, err := s.PutDomain(ctx, region, sampleDomain("api"), "create", "test", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)

	// Get
	d, rv, err := s.GetDomain(ctx, region, "api")
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "api", d.Name)
	assert.Equal(t, []string{"api.example.com"}, d.Hosts)
	assert.Equal(t, int64(1), rv)

	// List
	domains, err := s.ListDomains(ctx, region)
	require.NoError(t, err)
	assert.Len(t, domains, 1)

	// Update (with OCC)
	updated := sampleDomain("api")
	updated.Hosts = []string{"api-v2.example.com"}
	ver2, err := s.PutDomain(ctx, region, updated, "update", "test", rv)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ver2)

	d2, rv2, _ := s.GetDomain(ctx, region, "api")
	assert.Equal(t, []string{"api-v2.example.com"}, d2.Hosts)
	assert.Equal(t, int64(2), rv2)

	// Delete
	ver3, err := s.DeleteDomain(ctx, region, "api", "test")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver3)

	d3, _, err := s.GetDomain(ctx, region, "api")
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

// Cluster CRUD Tests
func TestClusterCRUD(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	ver, err := s.PutCluster(ctx, region, sampleCluster("backend"), "create", "test", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)

	c, rv, err := s.GetCluster(ctx, region, "backend")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "backend", c.Name)
	assert.Equal(t, "roundrobin", c.LBType)
	assert.Equal(t, int64(1), rv)

	clusters, err := s.ListClusters(ctx, region)
	require.NoError(t, err)
	assert.Len(t, clusters, 1)

	// Delete
	_, err = s.DeleteCluster(ctx, region, "backend", "test")
	require.NoError(t, err)
	c2, _, _ := s.GetCluster(ctx, region, "backend")
	assert.Nil(t, c2)
}

// History & Rollback Tests
func TestDomainHistory(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create v1
	d := sampleDomain("hist")
	s.PutDomain(ctx, region, d, "create", "alice", 0)

	// Update v2
	d.Hosts = []string{"hist-v2.example.com"}
	s.PutDomain(ctx, region, d, "update", "bob", 1)

	// Get history
	history, err := s.GetDomainHistory(ctx, region, "hist")
	require.NoError(t, err)
	assert.Len(t, history, 2)
	assert.Equal(t, int64(2), history[0].Version) // newest first
	assert.Equal(t, "update", history[0].Action)
	assert.Equal(t, "bob", history[0].Operator)
	assert.Equal(t, int64(1), history[1].Version)
	assert.Equal(t, "create", history[1].Action)

	// Get specific version
	v1, err := s.GetDomainVersion(ctx, region, "hist", 1)
	require.NoError(t, err)
	require.NotNil(t, v1)
	assert.Equal(t, "create", v1.Action)

	// Rollback to v1
	ver, err := s.RollbackDomain(ctx, region, "hist", 1, "charlie")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver) // v3 is the rollback

	d2, _, _ := s.GetDomain(ctx, region, "hist")
	assert.Equal(t, "hist.example.com", d2.Hosts[0])
}

func TestClusterHistory(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"
	c := sampleCluster("hist-cluster")
	s.PutCluster(ctx, region, c, "create", "alice", 0)

	c.LBType = "random"
	s.PutCluster(ctx, region, c, "update", "bob", 1)

	history, err := s.GetClusterHistory(ctx, region, "hist-cluster")
	require.NoError(t, err)
	assert.Len(t, history, 2)

	// Rollback
	ver, err := s.RollbackCluster(ctx, region, "hist-cluster", 1, "charlie")
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver)

	c2, _, _ := s.GetCluster(ctx, region, "hist-cluster")
	assert.Equal(t, "roundrobin", c2.LBType)
}

// OCC (Optimistic Concurrency Control) Tests
func TestDomainOCC_CreateConflict(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// First create succeeds
	ver, err := s.PutDomain(ctx, region, sampleDomain("occ"), "create", "alice", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)

	// Second create with expectedVersion=0 should conflict
	_, err = s.PutDomain(ctx, region, sampleDomain("occ"), "create", "bob", 0)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestDomainOCC_UpdateStaleVersion(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	s.PutDomain(ctx, region, sampleDomain("occ2"), "create", "alice", 0)

	// Alice reads rv=1
	_, rv1, _ := s.GetDomain(ctx, region, "occ2")
	assert.Equal(t, int64(1), rv1)

	// Bob updates with rv=1 → succeeds
	d := sampleDomain("occ2")
	d.Hosts = []string{"bob.example.com"}
	_, err := s.PutDomain(ctx, region, d, "update", "bob", rv1)
	require.NoError(t, err)

	// Alice tries to update with stale rv=1 → conflict
	d2 := sampleDomain("occ2")
	d2.Hosts = []string{"alice.example.com"}
	_, err = s.PutDomain(ctx, region, d2, "update", "alice", rv1)
	assert.ErrorIs(t, err, ErrConflict)

	// Verify Bob's update persisted
	got, rv2, _ := s.GetDomain(ctx, region, "occ2")
	assert.Equal(t, []string{"bob.example.com"}, got.Hosts)
	assert.Equal(t, int64(2), rv2)
}

func TestDomainOCC_BypassMode(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create with bypass (-1)
	_, err := s.PutDomain(ctx, region, sampleDomain("bypass"), "create", "test", -1)
	require.NoError(t, err)

	// Update with bypass (-1) regardless of version
	d := sampleDomain("bypass")
	d.Hosts = []string{"bypass-v2.example.com"}
	_, err = s.PutDomain(ctx, region, d, "update", "test", -1)
	require.NoError(t, err)

	got, _, _ := s.GetDomain(ctx, region, "bypass")
	assert.Equal(t, []string{"bypass-v2.example.com"}, got.Hosts)
}

func TestClusterOCC_CreateConflict(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	_, err := s.PutCluster(ctx, region, sampleCluster("occ-c"), "create", "alice", 0)
	require.NoError(t, err)

	_, err = s.PutCluster(ctx, region, sampleCluster("occ-c"), "create", "bob", 0)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestClusterOCC_UpdateStaleVersion(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	s.PutCluster(ctx, region, sampleCluster("occ-c2"), "create", "alice", 0)
	_, rv1, _ := s.GetCluster(ctx, region, "occ-c2")

	// Bob updates
	c := sampleCluster("occ-c2")
	c.LBType = "random"
	_, err := s.PutCluster(ctx, region, c, "update", "bob", rv1)
	require.NoError(t, err)

	// Alice tries stale version
	c2 := sampleCluster("occ-c2")
	c2.LBType = "least_request"
	_, err = s.PutCluster(ctx, region, c2, "update", "alice", rv1)
	assert.ErrorIs(t, err, ErrConflict)

	got, _, _ := s.GetCluster(ctx, region, "occ-c2")
	assert.Equal(t, "random", got.LBType)
}

// Watch & Revision Tests
func TestWatchFrom(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Initial revision should be 0
	rev, err := s.CurrentRevision(ctx, region)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rev)

	// Create some data
	s.PutDomain(ctx, region, sampleDomain("watch1"), "create", "test", 0)
	s.PutCluster(ctx, region, sampleCluster("watch-c1"), "create", "test", 0)

	// Watch from 0
	events, maxRev, err := s.WatchFrom(ctx, region, 0)
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.True(t, maxRev > 0)

	// Watch from maxRev should return no events
	events2, _, err := s.WatchFrom(ctx, region, maxRev)
	require.NoError(t, err)
	assert.Empty(t, events2)

	// One more change
	s.PutDomain(ctx, region, sampleDomain("watch2"), "create", "test", 0)
	events3, _, err := s.WatchFrom(ctx, region, maxRev)
	require.NoError(t, err)
	assert.Len(t, events3, 1)
	assert.Equal(t, "domain", events3[0].Kind)
	assert.Equal(t, "watch2", events3[0].Name)
}

// Region Tests
func TestRegions(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	// Default region should exist
	nsList, err := s.ListRegions(ctx)
	require.NoError(t, err)
	assert.Contains(t, nsList, "default")

	// Create new region
	err = s.CreateRegion(ctx, "production")
	require.NoError(t, err)

	nsList, err = s.ListRegions(ctx)
	require.NoError(t, err)
	assert.Contains(t, nsList, "production")

	// Region isolation: data in one region doesn't appear in another
	s.PutDomain(ctx, "default", sampleDomain("d1"), "create", "test", 0)
	s.PutDomain(ctx, "production", sampleDomain("d2"), "create", "test", 0)

	defaultDomains, _ := s.ListDomains(ctx, "default")
	prodDomains, _ := s.ListDomains(ctx, "production")
	assert.Len(t, defaultDomains, 1)
	assert.Equal(t, "d1", defaultDomains[0].Name)
	assert.Len(t, prodDomains, 1)
	assert.Equal(t, "d2", prodDomains[0].Name)
}

// Bulk Config Tests
func TestPutAllConfig(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Pre-populate
	s.PutDomain(ctx, region, sampleDomain("old"), "create", "test", 0)
	s.PutCluster(ctx, region, sampleCluster("old-c"), "create", "test", 0)

	// Replace all
	newDomains := []model.DomainConfig{*sampleDomain("new1"), *sampleDomain("new2")}
	newClusters := []model.ClusterConfig{*sampleCluster("new-c")}
	_, err := s.PutAllConfig(ctx, region, newDomains, newClusters, "import-test")
	require.NoError(t, err)

	// Old data should be gone
	d, _, _ := s.GetDomain(ctx, region, "old")
	assert.Nil(t, d)
	c, _, _ := s.GetCluster(ctx, region, "old-c")
	assert.Nil(t, c)

	// New data should exist
	domains, _ := s.ListDomains(ctx, region)
	assert.Len(t, domains, 2)
	clusters, _ := s.ListClusters(ctx, region)
	assert.Len(t, clusters, 1)
}

// Audit Log Tests
func TestAuditLog(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	s.PutDomain(ctx, region, sampleDomain("audit1"), "create", "alice", 0)
	s.PutDomain(ctx, region, sampleDomain("audit2"), "create", "bob", 0)
	s.DeleteDomain(ctx, region, "audit1", "charlie")

	entries, total, err := s.ListAuditLog(ctx, region, 50, 0)
	require.NoError(t, err)
	assert.True(t, total >= 3)
	assert.True(t, len(entries) >= 3)
}

// API Credentials Tests
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
	result, err := s.CreateAPICredential(ctx, region, cred)
	require.NoError(t, err)
	assert.True(t, result.ID > 0)
	assert.Equal(t, region, result.Region)

	// List
	creds, err := s.ListAPICredentials(ctx, region)
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
	err = s.UpdateAPICredential(ctx, region, found)
	require.NoError(t, err)

	// Delete
	err = s.DeleteAPICredential(ctx, region, found.ID)
	require.NoError(t, err)

	creds2, _ := s.ListAPICredentials(ctx, region)
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

// Gateway Status Tests
func TestGatewayInstanceStatus(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"
	instances := []GatewayInstanceStatus{
		{ID: "gw-1", Status: "running", ConfigRevision: 10},
		{ID: "gw-2", Status: "running", ConfigRevision: 10},
	}

	err := s.UpsertGatewayInstances(ctx, region, instances)
	require.NoError(t, err)

	list, err := s.ListGatewayInstances(ctx, region)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// Update: remove one
	err = s.UpsertGatewayInstances(ctx, region, instances[:1])
	require.NoError(t, err)

	list2, _ := s.ListGatewayInstances(ctx, region)
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
		IsLeader:        true,
		StartedAt:       time.Now().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().Format(time.RFC3339),
		ConfigRevision:  42,
	}

	err := s.UpsertControllerStatus(ctx, region, ctrl)
	require.NoError(t, err)

	got, err := s.GetControllerStatus(ctx, region)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "ctrl-1", got.ID)
	assert.Equal(t, "running", got.Status)
	assert.True(t, got.IsLeader)
	assert.Equal(t, int64(42), got.ConfigRevision)

	// Update: lose leadership
	ctrl.IsLeader = false
	err = s.UpsertControllerStatus(ctx, region, ctrl)
	require.NoError(t, err)

	got, err = s.GetControllerStatus(ctx, region)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.IsLeader)
}

// Grafana Dashboards Tests
func TestGrafanaDashboards(t *testing.T) {
	ctx := context.Background()
	s, cleanup := startPostgres(t, ctx)
	defer cleanup()

	ns := "default"

	// Create
	d1, err := s.PutGrafanaDashboard(ctx, region, &GrafanaDashboard{
		Name: "Overview",
		URL:  "https://grafana.example.com/d/abc123",
	})
	require.NoError(t, err)
	assert.True(t, d1.ID > 0)

	// List
	dashboards, err := s.ListGrafanaDashboards(ctx, region)
	require.NoError(t, err)
	assert.Len(t, dashboards, 1)

	// Update
	d1.Name = "Updated Overview"
	_, err = s.PutGrafanaDashboard(ctx, region, d1)
	require.NoError(t, err)

	// Delete
	err = s.DeleteGrafanaDashboard(ctx, region, d1.ID)
	require.NoError(t, err)

	dashboards2, _ := s.ListGrafanaDashboards(ctx, region)
	assert.Empty(t, dashboards2)
}

// Scope / Role Tests
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

func TestValidateRegionName(t *testing.T) {
	assert.Empty(t, ValidateRegionName("default"))
	assert.Empty(t, ValidateRegionName("production"))
	assert.Empty(t, ValidateRegionName("my-region-123"))
	assert.Empty(t, ValidateRegionName("a"))
	assert.NotEmpty(t, ValidateRegionName(""))
	assert.NotEmpty(t, ValidateRegionName("-bad"))
	assert.NotEmpty(t, ValidateRegionName("bad-"))
	assert.NotEmpty(t, ValidateRegionName("UPPERCASE"))
	assert.NotEmpty(t, ValidateRegionName("has space"))

	// 63 chars should be ok
	assert.Empty(t, ValidateRegionName("a"+strings.Repeat("b", 61)+"c"))
	// 64 chars should fail
	assert.NotEmpty(t, ValidateRegionName(strings.Repeat("a", 64)))
}
