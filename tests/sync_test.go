package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ══════════════════════════════════════════════════════════════════════
//  Server + Controller Sync Tests
//
//  These tests exercise the Server → Controller → etcd pipeline.
//  The server is used as a black-box HTTP API; config is created/modified
//  via CRUD calls. The real controller binary syncs to etcd.
//
//  Key scenarios:
//    1. CRUD API → controller sync to etcd (no auth)
//    2. CRUD API → controller sync to etcd (HMAC auth)
//    3. Config drift in etcd is auto-corrected by controller
//    4. Bulk config replacement syncs correctly
//    5. Gateway instances registered in etcd sync back to server
//    6. Namespace-scoped sync isolation
// ══════════════════════════════════════════════════════════════════════

// TestE2E_Sync_BasicCRUDToEtcd creates config via the server API and
// verifies the controller syncs it to etcd, including incremental add/delete.
func TestE2E_Sync_BasicCRUDToEtcd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// ── Create config via API ──
	resp := apiPost(t, base, "/api/v1/domains", domainConfig{
		Name:  "web",
		Hosts: []string{"web.example.com"},
		Routes: []routeConfig{
			{Name: "all", URI: "/*", Clusters: []weightedCluster{{Name: "web-be", Weight: 100}}, Status: 1},
		},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = apiPost(t, base, "/api/v1/clusters", clusterConfig{
		Name: "web-be", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 3, Send: 6, Read: 6},
		Nodes: []upstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = apiPost(t, base, "/api/v1/domains", domainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []routeConfig{
			{Name: "catch-all", URI: "/*", Clusters: []weightedCluster{{Name: "api-be", Weight: 100}}, Status: 1},
		},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = apiPost(t, base, "/api/v1/clusters", clusterConfig{
		Name: "api-be", LBType: "random", Timeout: timeoutConfig{Connect: 5, Send: 10, Read: 10},
		Nodes: []upstreamNode{{Host: "10.0.0.2", Port: 9090, Weight: 50}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ── Start controller ──
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL:             base,
		etcdEndpoint:      etcdEndpoint,
		pollInterval:      1,
		reconcileInterval: 3,
	})
	defer ctrl.stop()

	// ── Verify initial sync ──
	waitForEtcdCount(t, etcdClient, "/hermes/domains/", 2, 15*time.Second)
	waitForEtcdCount(t, etcdClient, "/hermes/clusters/", 2, 15*time.Second)

	domainResp, err := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	require.NoError(t, err)
	domainNames := extractKeyNames(domainResp)
	assert.True(t, domainNames["web"])
	assert.True(t, domainNames["api"])

	// Verify JSON content
	for _, kv := range domainResp.Kvs {
		var cfg map[string]any
		require.NoError(t, json.Unmarshal(kv.Value, &cfg))
		assert.NotEmpty(t, cfg["name"])
		assert.NotNil(t, cfg["hosts"])
	}

	clusterResp, err := etcdClient.Get(ctx, "/hermes/clusters/", clientv3.WithPrefix())
	require.NoError(t, err)
	clusterNames := extractKeyNames(clusterResp)
	assert.True(t, clusterNames["web-be"])
	assert.True(t, clusterNames["api-be"])

	// ── Incremental: delete + add ──
	resp = apiDelete(t, base, "/api/v1/domains/api")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = apiPost(t, base, "/api/v1/domains", domainConfig{
		Name:  "admin",
		Hosts: []string{"admin.example.com"},
		Routes: []routeConfig{
			{Name: "all", URI: "/*", Clusters: []weightedCluster{{Name: "web-be", Weight: 100}}, Status: 1},
		},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	waitForEtcdKey(t, etcdClient, "/hermes/domains/admin", 15*time.Second)
	waitForEtcdKeyGone(t, etcdClient, "/hermes/domains/api", 15*time.Second)

	domainResp2, _ := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	names := extractKeyNames(domainResp2)
	assert.True(t, names["web"])
	assert.True(t, names["admin"])
	assert.False(t, names["api"])

	// ── Incremental: add new cluster ──
	resp = apiPost(t, base, "/api/v1/clusters", clusterConfig{
		Name: "new-cluster", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Send: 2, Read: 2},
		Nodes: []upstreamNode{{Host: "10.0.0.3", Port: 8080, Weight: 100}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	waitForEtcdKey(t, etcdClient, "/hermes/clusters/new-cluster", 15*time.Second)

	// ── Verify revision is published ──
	waitForEtcdKey(t, etcdClient, "/hermes/meta/config_revision", 15*time.Second)
}

// TestE2E_Sync_AuthenticatedPipeline tests controller sync with HMAC authentication.
func TestE2E_Sync_AuthenticatedPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// ── Bootstrap credentials ──
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin",
		"scopes":      []string{"config:read", "config:write", "credential:read", "credential:write", "audit:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	adminCred := readJSON(t, resp)
	adminAK := adminCred["access_key"].(string)
	adminSK := adminCred["secret_key"].(string)

	// Unauthenticated access blocked
	resp = apiGet(t, base, "/api/v1/domains")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// Create controller credential
	resp = hmacRequest(t, "POST", base+"/api/v1/credentials", adminAK, adminSK, map[string]any{
		"description": "controller",
		"scopes":      []string{"config:read", "config:watch", "status:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	ctrlCred := readJSON(t, resp)
	ctrlAK := ctrlCred["access_key"].(string)
	ctrlSK := ctrlCred["secret_key"].(string)

	// ── Create config via HMAC ──
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", adminAK, adminSK, domainConfig{
		Name: "web", Hosts: []string{"web.example.com"},
		Routes: []routeConfig{{Name: "all", URI: "/*", Clusters: []weightedCluster{{Name: "web-be", Weight: 100}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "POST", base+"/api/v1/clusters", adminAK, adminSK, clusterConfig{
		Name: "web-be", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 3, Send: 6, Read: 6},
		Nodes: []upstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ── Start controller with HMAC ──
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL: base, etcdEndpoint: etcdEndpoint,
		accessKey: ctrlAK, secretKey: ctrlSK,
		pollInterval: 1, reconcileInterval: 3,
	})
	defer ctrl.stop()

	waitForEtcdKey(t, etcdClient, "/hermes/domains/web", 15*time.Second)
	waitForEtcdKey(t, etcdClient, "/hermes/clusters/web-be", 15*time.Second)

	// Verify content
	domainResp, _ := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	for _, kv := range domainResp.Kvs {
		var cfg map[string]any
		require.NoError(t, json.Unmarshal(kv.Value, &cfg))
		assert.NotEmpty(t, cfg["name"])
		assert.NotNil(t, cfg["routes"])
	}

	// ── Incremental: add + delete via HMAC ──
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", adminAK, adminSK, domainConfig{
		Name: "api", Hosts: []string{"api.example.com"},
		Routes: []routeConfig{{Name: "all", URI: "/*", Clusters: []weightedCluster{{Name: "web-be", Weight: 100}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	waitForEtcdKey(t, etcdClient, "/hermes/domains/api", 15*time.Second)

	resp = hmacRequest(t, "DELETE", base+"/api/v1/domains/api", adminAK, adminSK, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	waitForEtcdKeyGone(t, etcdClient, "/hermes/domains/api", 15*time.Second)

	// Verify revision published
	waitForEtcdKey(t, etcdClient, "/hermes/meta/config_revision", 15*time.Second)
}

// TestE2E_Sync_ControllerDeniedWithoutCredentials verifies that a controller
// without valid credentials cannot sync from an authenticated server.
func TestE2E_Sync_ControllerDeniedWithoutCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, _, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// Activate auth
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "activate-auth", "scopes": []string{"config:read"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Start controller WITHOUT credentials → should exit
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL:             base,
		etcdEndpoint:      etcdClient.Endpoints()[0],
		pollInterval:      1,
		reconcileInterval: 2,
	})

	done := make(chan error, 1)
	go func() { done <- ctrl.cmd.Wait() }()

	select {
	case err := <-done:
		assert.Error(t, err, "controller without credentials should exit with error")
	case <-time.After(15 * time.Second):
		ctrl.stop()
		t.Fatal("controller should have exited due to auth failure")
	}

	// etcd should be empty
	domainResp, _ := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	assert.Equal(t, 0, int(domainResp.Count))
}

// TestE2E_Sync_ConfigDriftCorrection tests that when etcd has stale, missing,
// or extra keys, the controller's reconcile loop auto-corrects them.
func TestE2E_Sync_ConfigDriftCorrection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// Create the desired config in the server
	resp := apiPost(t, base, "/api/v1/domains", domainConfig{
		Name: "correct", Hosts: []string{"correct.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "correct-be", Weight: 100}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = apiPost(t, base, "/api/v1/clusters", clusterConfig{
		Name: "correct-be", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Send: 2, Read: 2},
		Nodes: []upstreamNode{{Host: "10.0.0.1", Port: 80, Weight: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ── Pollute etcd with drift ──
	// 1. Dirty key: exists in etcd but NOT in server (should be deleted)
	_, err := etcdClient.Put(ctx, "/hermes/domains/dirty-domain", `{"name":"dirty-domain","hosts":["dirty.example.com"],"routes":[]}`)
	require.NoError(t, err)

	// 2. Stale key: exists in both but with wrong value (should be updated)
	_, err = etcdClient.Put(ctx, "/hermes/domains/correct", `{"name":"correct","hosts":["WRONG.example.com"],"routes":[]}`)
	require.NoError(t, err)

	// 3. Extra cluster in etcd not in server
	_, err = etcdClient.Put(ctx, "/hermes/clusters/orphan-cluster", `{"name":"orphan-cluster","type":"roundrobin"}`)
	require.NoError(t, err)

	// Verify drift is present
	dirtyResp, _ := etcdClient.Get(ctx, "/hermes/domains/dirty-domain")
	assert.Equal(t, 1, int(dirtyResp.Count), "dirty key should exist before reconcile")

	orphanResp, _ := etcdClient.Get(ctx, "/hermes/clusters/orphan-cluster")
	assert.Equal(t, 1, int(orphanResp.Count), "orphan key should exist before reconcile")

	// ── Start controller (will reconcile on startup) ──
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL:             base,
		etcdEndpoint:      etcdEndpoint,
		pollInterval:      1,
		reconcileInterval: 5,
	})
	defer ctrl.stop()

	// Wait for reconcile to clean up
	waitForEtcdKeyGone(t, etcdClient, "/hermes/domains/dirty-domain", 15*time.Second)
	waitForEtcdKeyGone(t, etcdClient, "/hermes/clusters/orphan-cluster", 15*time.Second)

	// Verify "correct" domain was updated to the right value
	correctResp, err := etcdClient.Get(ctx, "/hermes/domains/correct")
	require.NoError(t, err)
	require.Equal(t, 1, int(correctResp.Count))
	var correctCfg map[string]any
	json.Unmarshal(correctResp.Kvs[0].Value, &correctCfg)
	hosts := correctCfg["hosts"].([]any)
	assert.Equal(t, "correct.example.com", hosts[0], "stale value should be corrected")

	// Verify correct-be cluster exists
	clusterResp, _ := etcdClient.Get(ctx, "/hermes/clusters/correct-be")
	assert.Equal(t, 1, int(clusterResp.Count))

	// Total: exactly 1 domain + 1 cluster
	allDomains, _ := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	assert.Equal(t, 1, int(allDomains.Count), "should have exactly 1 domain after drift correction")

	allClusters, _ := etcdClient.Get(ctx, "/hermes/clusters/", clientv3.WithPrefix())
	assert.Equal(t, 1, int(allClusters.Count), "should have exactly 1 cluster after drift correction")
}

// TestE2E_Sync_BulkConfigReplace tests PUT /api/v1/config for bulk replacement
// and verifies the controller reconciles the full replacement to etcd.
func TestE2E_Sync_BulkConfigReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// Create initial config
	apiPost(t, base, "/api/v1/domains", domainConfig{
		Name: "old", Hosts: []string{"old.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "old-c", Weight: 100}}, Status: 1}},
	}).Body.Close()
	apiPost(t, base, "/api/v1/clusters", clusterConfig{
		Name: "old-c", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Read: 1},
		Nodes: []upstreamNode{{Host: "h", Port: 80, Weight: 1}},
	}).Body.Close()

	// Bulk replace
	newCfg := gatewayConfig{
		Domains: []domainConfig{
			{Name: "new1", Hosts: []string{"new1.example.com"}, Routes: []routeConfig{
				{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "new-c", Weight: 100}}, Status: 1},
			}},
			{Name: "new2", Hosts: []string{"new2.example.com"}, Routes: []routeConfig{
				{Name: "r2", URI: "/*", Clusters: []weightedCluster{{Name: "new-c", Weight: 100}}, Status: 1},
			}},
		},
		Clusters: []clusterConfig{
			{Name: "new-c", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Send: 2, Read: 2},
				Nodes: []upstreamNode{{Host: "10.0.0.1", Port: 80, Weight: 1}}},
		},
	}
	resp := apiPut(t, base, "/api/v1/config", newCfg)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Verify old gone from API
	resp = apiGet(t, base, "/api/v1/domains/old")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// Start controller
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL: base, etcdEndpoint: etcdEndpoint,
		pollInterval: 1, reconcileInterval: 3,
	})
	defer ctrl.stop()

	waitForEtcdCount(t, etcdClient, "/hermes/domains/", 2, 15*time.Second)
	waitForEtcdCount(t, etcdClient, "/hermes/clusters/", 1, 15*time.Second)

	// Old key should not be in etcd
	oldResp, _ := etcdClient.Get(ctx, "/hermes/domains/old")
	assert.Equal(t, 0, int(oldResp.Count))
}

// TestE2E_Sync_InstanceRegistrationSyncBackToServer tests that gateway instances
// registered in etcd are synced back to the server's status API by the controller.
func TestE2E_Sync_InstanceRegistrationSyncBackToServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)
	gwBin := buildGateway(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	base := srv.baseURL

	// Start controller (watches /hermes/instances/ and reports to server)
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL: base, etcdEndpoint: etcdEndpoint,
		pollInterval: 1, reconcileInterval: 60,
	})
	defer ctrl.stop()

	// Start gateway with instance_registry enabled
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, true)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	// Wait for gateway to register in etcd
	time.Sleep(3 * time.Second)

	// Verify instance in etcd
	instanceResp, err := etcdClient.Get(ctx, "/hermes/instances/", clientv3.WithPrefix())
	require.NoError(t, err)
	require.GreaterOrEqual(t, int(instanceResp.Count), 1, "gateway should register in etcd")

	// Wait for controller to report instances to server
	time.Sleep(5 * time.Second)

	// Query server status API
	resp := apiGet(t, base, "/api/v1/status/instances")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)

	instances := data["instances"].([]any)
	assert.GreaterOrEqual(t, len(instances), 1, "server should report at least 1 gateway instance")

	inst := instances[0].(map[string]any)
	assert.NotEmpty(t, inst["id"])
	assert.NotEmpty(t, inst["status"])

	// Query controller status
	resp = apiGet(t, base, "/api/v1/status/controller")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data = readJSON(t, resp)
	controller := data["controller"].(map[string]any)
	assert.NotEmpty(t, controller["id"])
	assert.Equal(t, "running", controller["status"])
}

// TestE2E_Sync_NamespaceIsolation verifies that a controller scoped to
// one namespace only syncs that namespace's config to its etcd prefix.
func TestE2E_Sync_NamespaceIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ctx := context.Background()
	srvBin := buildServer(t)
	ctrlBin := buildController(t)

	pgDSN, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()

	srv := startServerProc(t, srvBin, serverOpts{pgDSN: pgDSN})
	defer srv.stop()

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()

	base := srv.baseURL

	// Bootstrap credentials (also gets namespace:write for creating namespaces)
	resp := apiPost(t, base, "/api/v1/credentials", map[string]any{
		"description": "admin",
		"scopes":      []string{"config:read", "config:write", "credential:read", "credential:write", "namespace:read", "namespace:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	adminCred := readJSON(t, resp)
	adminAK := adminCred["access_key"].(string)
	adminSK := adminCred["secret_key"].(string)

	// Create "staging" namespace via API
	resp = hmacRequest(t, "POST", base+"/api/v1/namespaces", adminAK, adminSK, map[string]any{
		"name": "staging",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create config in default namespace
	resp = hmacRequest(t, "POST", base+"/api/v1/domains", adminAK, adminSK, domainConfig{
		Name: "default-domain", Hosts: []string{"default.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "default-be", Weight: 100}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequest(t, "POST", base+"/api/v1/clusters", adminAK, adminSK, clusterConfig{
		Name: "default-be", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Send: 2, Read: 2},
		Nodes: []upstreamNode{{Host: "10.0.0.1", Port: 80, Weight: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create credential + config in staging namespace
	resp = hmacRequestWithNS(t, "POST", base+"/api/v1/credentials", adminAK, adminSK, "staging", map[string]any{
		"description": "staging-ctrl",
		"scopes":      []string{"config:read", "config:watch", "status:write"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	stagingCred := readJSON(t, resp)
	stagingAK := stagingCred["access_key"].(string)
	stagingSK := stagingCred["secret_key"].(string)

	resp = hmacRequestWithNS(t, "POST", base+"/api/v1/domains", adminAK, adminSK, "staging", domainConfig{
		Name: "staging-domain", Hosts: []string{"staging.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "staging-be", Weight: 100}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = hmacRequestWithNS(t, "POST", base+"/api/v1/clusters", adminAK, adminSK, "staging", clusterConfig{
		Name: "staging-be", LBType: "roundrobin", Timeout: timeoutConfig{Connect: 1, Send: 2, Read: 2},
		Nodes: []upstreamNode{{Host: "10.0.0.2", Port: 80, Weight: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Start controller for STAGING namespace only
	ctrl := startControllerProc(t, ctrlBin, controllerOpts{
		cpURL:             base,
		etcdEndpoint:      etcdEndpoint,
		domainPrefix:      "/hermes/staging/domains",
		clusterPrefix:     "/hermes/staging/clusters",
		metaPrefix:        "/hermes/staging/meta",
		namespace:         "staging",
		accessKey:         stagingAK,
		secretKey:         stagingSK,
		pollInterval:      1,
		reconcileInterval: 3,
	})
	defer ctrl.stop()

	// Only staging config should appear
	waitForEtcdKey(t, etcdClient, "/hermes/staging/domains/staging-domain", 15*time.Second)
	waitForEtcdKey(t, etcdClient, "/hermes/staging/clusters/staging-be", 15*time.Second)

	stagingDomains, _ := etcdClient.Get(ctx, "/hermes/staging/domains/", clientv3.WithPrefix())
	assert.Equal(t, 1, int(stagingDomains.Count))
	stagingNames := extractKeyNames(stagingDomains)
	assert.True(t, stagingNames["staging-domain"])

	// Default namespace data should NOT be in staging prefix
	for _, kv := range stagingDomains.Kvs {
		assert.False(t, strings.Contains(string(kv.Key), "default-domain"))
	}
}

// TestE2E_Sync_ConfigValidation tests that invalid configs are rejected by the API.
func TestE2E_Sync_ConfigValidation(t *testing.T) {
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
	_ = ctx

	// Missing hosts → 400
	resp := apiPost(t, base, "/api/v1/domains", domainConfig{
		Name: "bad", Hosts: []string{},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}}},
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	data := readJSON(t, resp)
	assert.NotNil(t, data["errors"])

	// Missing name → 400
	resp = apiPost(t, base, "/api/v1/domains", domainConfig{
		Hosts:  []string{"a.com"},
		Routes: []routeConfig{{Name: "r", URI: "/", Clusters: []weightedCluster{{Name: "c", Weight: 1}}}},
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Cluster missing name → 400
	resp = apiPost(t, base, "/api/v1/clusters", clusterConfig{LBType: "roundrobin"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestE2E_Sync_ConfigWatch tests the revision and watch endpoints used by the controller.
func TestE2E_Sync_ConfigWatch(t *testing.T) {
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
	_ = ctx

	// Create some config to generate revisions
	resp := apiPost(t, base, "/api/v1/domains", domainConfig{
		Name: "watch-test", Hosts: []string{"watch.example.com"},
		Routes: []routeConfig{{Name: "r", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Get revision
	resp = apiGet(t, base, "/api/v1/config/revision")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data := readJSON(t, resp)
	rev := data["revision"].(float64)
	assert.True(t, rev > 0)

	// Watch changes since revision 0
	resp = apiGet(t, base, "/api/v1/config/watch?revision=0")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data = readJSON(t, resp)
	total := data["total"].(float64)
	assert.True(t, total > 0)

	// Get full config
	resp = apiGet(t, base, "/api/v1/config")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	data = readJSON(t, resp)
	cfgMap := data["config"].(map[string]any)
	assert.NotNil(t, cfgMap["domains"])
	assert.NotNil(t, cfgMap["clusters"])
}

// TestE2E_Sync_NamespaceIsolationAPI verifies namespace isolation at the API level.
func TestE2E_Sync_NamespaceIsolationAPI(t *testing.T) {
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

	// Create namespace via API (bootstrap mode - no credentials yet, so unauthenticated is allowed)
	resp := apiPost(t, base, "/api/v1/namespaces", map[string]any{
		"name": "production",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create data in default namespace
	resp = apiPost(t, base, "/api/v1/domains", domainConfig{
		Name: "default-domain", Hosts: []string{"default.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create data in production namespace via header
	req, _ := http.NewRequest("POST", base+"/api/v1/domains", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hermes-Namespace", "production")
	b, _ := json.Marshal(domainConfig{
		Name: "prod-domain", Hosts: []string{"prod.example.com"},
		Routes: []routeConfig{{Name: "r1", URI: "/*", Clusters: []weightedCluster{{Name: "c", Weight: 1}}, Status: 1}},
	})
	req.Body = io.NopCloser(strings.NewReader(string(b)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Default ns: 1 domain
	resp = apiGet(t, base, "/api/v1/domains")
	data := readJSON(t, resp)
	assert.Equal(t, float64(1), data["total"])

	// Production ns: 1 domain
	req, _ = http.NewRequest("GET", base+"/api/v1/domains", nil)
	req.Header.Set("X-Hermes-Namespace", "production")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	data = readJSON(t, resp)
	assert.Equal(t, float64(1), data["total"])
}
