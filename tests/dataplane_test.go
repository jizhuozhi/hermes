package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ══════════════════════════════════════════════════════════════════════
//  P00 — Gateway Dataplane Tests
//
//  All tests in this file write config directly to etcd (the gateway's
//  only source of truth) and exercise the real gateway binary.
//  They do NOT depend on the server or controller.
// ══════════════════════════════════════════════════════════════════════

// putDomainJSON is a helper that writes a domain config JSON to etcd.
func putDomainJSON(t *testing.T, etcdClient *clientv3.Client, name, jsonStr string) {
	t.Helper()
	_, err := etcdClient.Put(context.Background(), "/hermes/domains/"+name, jsonStr)
	require.NoError(t, err)
}

// putClusterJSON is a helper that writes a cluster config JSON to etcd.
func putClusterJSON(t *testing.T, etcdClient *clientv3.Client, name, jsonStr string) {
	t.Helper()
	_, err := etcdClient.Put(context.Background(), "/hermes/clusters/"+name, jsonStr)
	require.NoError(t, err)
}

// proxyRequest sends an HTTP request through the gateway and returns the response.
func proxyRequest(t *testing.T, proxyBase, method, path, host string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, proxyBase+path, nil)
	require.NoError(t, err)
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ── Static Node Cluster ─────────────────────────────────────────────

// TestE2E_Dataplane_StaticNodeProxy tests the gateway proxying to a cluster
// with statically configured upstream nodes.
func TestE2E_Dataplane_StaticNodeProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	// Write config directly to etcd
	putDomainJSON(t, etcdClient, "web", fmt.Sprintf(`{
		"name": "web",
		"hosts": ["web.test.local"],
		"routes": [{
			"name": "all", "uri": "/*", "status": 1,
			"clusters": [{"name": "web-be", "weight": 100}]
		}]
	}`))
	putClusterJSON(t, etcdClient, "web-be", fmt.Sprintf(`{
		"name": "web-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	// Start gateway
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Proxy request → upstream
	resp := proxyRequest(t, proxyBase, "GET", "/hello/world", "web.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "hello from upstream", body["message"])
	assert.Equal(t, "/hello/world", body["path"])

	// Unknown host → 404
	resp = proxyRequest(t, proxyBase, "GET", "/test", "unknown.host", nil)
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()

	// Admin endpoints
	adminBase := "http://" + gw.adminAddr
	r, err := http.Get(adminBase + "/health")
	require.NoError(t, err)
	assert.Equal(t, 200, r.StatusCode)
	r.Body.Close()

	r, err = http.Get(adminBase + "/ready")
	require.NoError(t, err)
	assert.Equal(t, 200, r.StatusCode)
	readyBody := readJSON(t, r)
	assert.GreaterOrEqual(t, readyBody["domains"], float64(1))

	r, err = http.Get(adminBase + "/routes")
	require.NoError(t, err)
	assert.Equal(t, 200, r.StatusCode)
	r.Body.Close()
}

// ── Consul Service Discovery ────────────────────────────────────────

// TestE2E_Dataplane_ConsulDiscovery tests clusters with discovery_type=consul.
// The gateway should resolve nodes from Consul and proxy traffic to them.
func TestE2E_Dataplane_ConsulDiscovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	// Register upstream in Consul
	consulRegisterService(t, consulAddr, "api-svc", upHost, upPort, map[string]string{
		"env":    "test",
		"weight": "100",
	})

	// Write consul-discovery cluster config to etcd
	putDomainJSON(t, etcdClient, "api", `{
		"name": "api",
		"hosts": ["api.test.local"],
		"routes": [{
			"name": "all", "uri": "/*", "status": 1,
			"clusters": [{"name": "api-consul", "weight": 100}]
		}]
	}`)
	putClusterJSON(t, etcdClient, "api-consul", `{
		"name": "api-consul",
		"type": "roundrobin",
		"discovery_type": "consul",
		"service_name": "api-svc",
		"timeout": {"connect": 3, "send": 6, "read": 6}
	}`)

	// Start gateway
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)
	time.Sleep(3 * time.Second) // allow consul poll

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Proxy request → should reach upstream via consul-discovered nodes
	resp := proxyRequest(t, proxyBase, "GET", "/api/test", "api.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "hello from upstream", body["message"])

	// Deregister from Consul → gateway should lose the node
	consulDeregisterService(t, consulAddr, fmt.Sprintf("api-svc-%s-%d", upHost, upPort))
	time.Sleep(5 * time.Second)

	resp = proxyRequest(t, proxyBase, "GET", "/api/test", "api.test.local", nil)
	assert.True(t, resp.StatusCode == 502 || resp.StatusCode == 503,
		"expected 502/503 after consul deregister, got %d", resp.StatusCode)
	resp.Body.Close()
}

// ── Host Matching ───────────────────────────────────────────────────

// TestE2E_Dataplane_HostMatching tests exact host, wildcard suffix (*.example.com),
// wildcard prefix (api.*), and default catch-all (_).
func TestE2E_Dataplane_HostMatching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	clusterJSON := fmt.Sprintf(`{
		"name": "be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort)
	putClusterJSON(t, etcdClient, "be", clusterJSON)

	// Exact host match
	putDomainJSON(t, etcdClient, "exact", `{
		"name": "exact",
		"hosts": ["exact.test.local"],
		"routes": [{"name": "r", "uri": "/*", "status": 1, "clusters": [{"name": "be", "weight": 100}]}]
	}`)

	// Wildcard suffix: *.example.com
	putDomainJSON(t, etcdClient, "wildcard-suffix", `{
		"name": "wildcard-suffix",
		"hosts": ["*.example.com"],
		"routes": [{"name": "r", "uri": "/*", "status": 1, "clusters": [{"name": "be", "weight": 100}]}]
	}`)

	// Wildcard prefix: api.*
	putDomainJSON(t, etcdClient, "wildcard-prefix", `{
		"name": "wildcard-prefix",
		"hosts": ["api.*"],
		"routes": [{"name": "r", "uri": "/*", "status": 1, "clusters": [{"name": "be", "weight": 100}]}]
	}`)

	// Default catch-all: _
	putDomainJSON(t, etcdClient, "default", `{
		"name": "default",
		"hosts": ["_"],
		"routes": [{"name": "r", "uri": "/*", "status": 1, "clusters": [{"name": "be", "weight": 100}]}]
	}`)

	// Start gateway
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Exact host match
	resp := proxyRequest(t, proxyBase, "GET", "/test", "exact.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Wildcard suffix: sub.example.com → matches *.example.com
	resp = proxyRequest(t, proxyBase, "GET", "/test", "sub.example.com", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Wildcard prefix: api.anything → matches api.*
	resp = proxyRequest(t, proxyBase, "GET", "/test", "api.anything", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Default catch-all: random host → matches _
	resp = proxyRequest(t, proxyBase, "GET", "/test", "random.host.xyz", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

// ── Route URI Matching ──────────────────────────────────────────────

// TestE2E_Dataplane_RouteURIMatching tests exact URI, prefix wildcard,
// and priority ordering between routes.
func TestE2E_Dataplane_RouteURIMatching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	// Two upstreams to distinguish which route was matched
	upHost1, upPort1, upClose1 := startUpstreamMock(t)
	defer upClose1()
	upHost2, upPort2, upClose2 := startUpstreamMock(t)
	defer upClose2()

	putClusterJSON(t, etcdClient, "exact-be", fmt.Sprintf(`{
		"name": "exact-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost1, upPort1))

	putClusterJSON(t, etcdClient, "prefix-be", fmt.Sprintf(`{
		"name": "prefix-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost2, upPort2))

	// Domain with multiple routes: exact /v1/health > prefix /v1/* > catch-all /*
	putDomainJSON(t, etcdClient, "routing", fmt.Sprintf(`{
		"name": "routing",
		"hosts": ["routing.test.local"],
		"routes": [
			{
				"name": "exact-health",
				"uri": "/v1/health",
				"status": 1,
				"priority": 100,
				"clusters": [{"name": "exact-be", "weight": 100}]
			},
			{
				"name": "v1-prefix",
				"uri": "/v1/*",
				"status": 1,
				"priority": 50,
				"clusters": [{"name": "prefix-be", "weight": 100}]
			},
			{
				"name": "catch-all",
				"uri": "/*",
				"status": 1,
				"priority": 1,
				"clusters": [{"name": "prefix-be", "weight": 100}]
			}
		]
	}`))

	// Start gateway
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Exact match: /v1/health → exact-be (upHost1)
	resp := proxyRequest(t, proxyBase, "GET", "/v1/health", "routing.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	var body1 map[string]any
	json.NewDecoder(resp.Body).Decode(&body1)
	resp.Body.Close()
	assert.Equal(t, "/v1/health", body1["path"])

	// Prefix match: /v1/users → v1-prefix → prefix-be (upHost2)
	resp = proxyRequest(t, proxyBase, "GET", "/v1/users", "routing.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	var body2 map[string]any
	json.NewDecoder(resp.Body).Decode(&body2)
	resp.Body.Close()
	assert.Equal(t, "/v1/users", body2["path"])

	// Catch-all: /other → catch-all route
	resp = proxyRequest(t, proxyBase, "GET", "/other", "routing.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

// ── Header-Based Matching ───────────────────────────────────────────

// TestE2E_Dataplane_HeaderMatching tests routes that require specific header values.
func TestE2E_Dataplane_HeaderMatching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	putClusterJSON(t, etcdClient, "hdr-be", fmt.Sprintf(`{
		"name": "hdr-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	// Route with header matchers: exact X-Version: v2 (high priority)
	// + a catch-all route (low priority)
	putDomainJSON(t, etcdClient, "hdr-test", `{
		"name": "hdr-test",
		"hosts": ["hdr.test.local"],
		"routes": [
			{
				"name": "v2-only",
				"uri": "/api/*",
				"status": 1,
				"priority": 100,
				"headers": [
					{"name": "X-Version", "value": "v2", "match_type": "exact"}
				],
				"clusters": [{"name": "hdr-be", "weight": 100}]
			},
			{
				"name": "catch-all",
				"uri": "/*",
				"status": 1,
				"priority": 1,
				"clusters": [{"name": "hdr-be", "weight": 100}]
			}
		]
	}`)

	// Start gateway
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// With matching header → should match v2-only route (200)
	resp := proxyRequest(t, proxyBase, "GET", "/api/data", "hdr.test.local",
		map[string]string{"X-Version": "v2"})
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "/api/data", body["path"])

	// Without header → should fall through to catch-all (still 200, but different route)
	resp = proxyRequest(t, proxyBase, "GET", "/api/data", "hdr.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Different path not matched by v2-only → catch-all
	resp = proxyRequest(t, proxyBase, "GET", "/other", "hdr.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

// ── Rate Limiting (Token Bucket) ────────────────────────────────────

// TestE2E_Dataplane_RateLimit_TokenBucket tests rate limiting with mode=req (token bucket).
func TestE2E_Dataplane_RateLimit_TokenBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	putDomainJSON(t, etcdClient, "rl-req", fmt.Sprintf(`{
		"name": "rl-req",
		"hosts": ["rl-req.test.local"],
		"routes": [{
			"name": "limited",
			"uri": "/*",
			"status": 1,
			"clusters": [{"name": "rl-be", "weight": 100}],
			"rate_limit": {
				"mode": "req",
				"rate": 5.0,
				"burst": 2,
				"key": "route",
				"rejected_code": 429
			}
		}]
	}`))
	putClusterJSON(t, etcdClient, "rl-be", fmt.Sprintf(`{
		"name": "rl-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, true)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)
	time.Sleep(2 * time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	ok, limited := 0, 0
	for i := 0; i < 20; i++ {
		resp := proxyRequest(t, proxyBase, "GET", "/anything", "rl-req.test.local", nil)
		io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			ok++
		} else if resp.StatusCode == 429 {
			limited++
		}
	}

	assert.Greater(t, ok, 0, "at least some requests should succeed")
	assert.Greater(t, limited, 0, "some requests should be rate-limited (429)")
	t.Logf("token bucket: %d OK, %d rate-limited out of 20", ok, limited)
}

// ── Rate Limiting (Sliding Window) ──────────────────────────────────

// TestE2E_Dataplane_RateLimit_SlidingWindow tests rate limiting with mode=count (sliding window).
func TestE2E_Dataplane_RateLimit_SlidingWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	putDomainJSON(t, etcdClient, "rl-count", fmt.Sprintf(`{
		"name": "rl-count",
		"hosts": ["rl-count.test.local"],
		"routes": [{
			"name": "windowed",
			"uri": "/*",
			"status": 1,
			"clusters": [{"name": "rl-count-be", "weight": 100}],
			"rate_limit": {
				"mode": "count",
				"count": 5,
				"time_window": 60,
				"key": "route",
				"rejected_code": 429
			}
		}]
	}`))
	putClusterJSON(t, etcdClient, "rl-count-be", fmt.Sprintf(`{
		"name": "rl-count-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, true)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)
	time.Sleep(2 * time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	ok, limited := 0, 0
	for i := 0; i < 15; i++ {
		resp := proxyRequest(t, proxyBase, "GET", "/anything", "rl-count.test.local", nil)
		io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			ok++
		} else if resp.StatusCode == 429 {
			limited++
		}
	}

	assert.Greater(t, ok, 0, "at least some requests should succeed")
	assert.Greater(t, limited, 0, "some requests should be rate-limited (429)")
	t.Logf("sliding window: %d OK, %d rate-limited out of 15", ok, limited)
}

// ── etcd Watch Hot-Reload ───────────────────────────────────────────

// TestE2E_Dataplane_EtcdWatchHotReload tests that the gateway picks up config
// changes from etcd dynamically without restart.
func TestE2E_Dataplane_EtcdWatchHotReload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	// Start gateway with empty etcd
	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Initially no routes → 404
	resp := proxyRequest(t, proxyBase, "GET", "/test", "dynamic.test.local", nil)
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()

	// Write config to etcd while gateway is running
	putDomainJSON(t, etcdClient, "dynamic", fmt.Sprintf(`{
		"name": "dynamic",
		"hosts": ["dynamic.test.local"],
		"routes": [{
			"name": "all", "uri": "/*", "status": 1,
			"clusters": [{"name": "dyn-be", "weight": 100}]
		}]
	}`))
	putClusterJSON(t, etcdClient, "dyn-be", fmt.Sprintf(`{
		"name": "dyn-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	// Wait for gateway watcher to pick up the change
	time.Sleep(3 * time.Second)

	// Now the route should be available
	resp = proxyRequest(t, proxyBase, "GET", "/test", "dynamic.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "hello from upstream", body["message"])

	// Delete domain from etcd → route should disappear
	_, err := etcdClient.Delete(ctx, "/hermes/domains/dynamic")
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	resp = proxyRequest(t, proxyBase, "GET", "/test", "dynamic.test.local", nil)
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()

	// Re-add → route comes back
	putDomainJSON(t, etcdClient, "dynamic", fmt.Sprintf(`{
		"name": "dynamic",
		"hosts": ["dynamic.test.local"],
		"routes": [{
			"name": "all", "uri": "/*", "status": 1,
			"clusters": [{"name": "dyn-be", "weight": 100}]
		}]
	}`))

	time.Sleep(3 * time.Second)

	resp = proxyRequest(t, proxyBase, "GET", "/test", "dynamic.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

// ── Instance Registration ───────────────────────────────────────────

// TestE2E_Dataplane_InstanceRegistration tests that the gateway registers itself
// to etcd and the instance count reflects running gateways.
func TestE2E_Dataplane_InstanceRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

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

	time.Sleep(2 * time.Second)
	instanceResp, err := etcdClient.Get(ctx, "/hermes/instances/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, int(instanceResp.Count), 1, "expected at least 1 gateway instance")

	for _, kv := range instanceResp.Kvs {
		var inst map[string]any
		require.NoError(t, json.Unmarshal(kv.Value, &inst), "instance data should be valid JSON")
		assert.NotEmpty(t, inst["id"])
		assert.NotEmpty(t, inst["status"])
	}

	// Start second gateway → instance count should increase
	tmpDir2 := t.TempDir()
	gwConfig2 := writeGatewayConfig(t, tmpDir2, etcdEndpoint, consulAddr, true)
	proxyPort2 := freePort(t)
	adminPort2 := freePort(t)
	gw2 := startGatewayProc(t, gwBin, gwConfig2,
		fmt.Sprintf("127.0.0.1:%d", proxyPort2),
		fmt.Sprintf("127.0.0.1:%d", adminPort2))
	defer gw2.stop()
	gw2.waitReady(t, 15*time.Second)

	time.Sleep(2 * time.Second)
	instanceResp2, err := etcdClient.Get(ctx, "/hermes/instances/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, int(instanceResp2.Count), 2, "expected at least 2 gateway instances")

	// Stop second gateway → instance should eventually be removed (lease TTL expiry)
	gw2.stop()
	time.Sleep(12 * time.Second)
	instanceResp3, err := etcdClient.Get(ctx, "/hermes/instances/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Equal(t, 1, int(instanceResp3.Count), "after stopping gw2, only 1 instance should remain")
}

// ── Disabled Route ──────────────────────────────────────────────────

// TestE2E_Dataplane_DisabledRoute tests that routes with status=0 are not served.
func TestE2E_Dataplane_DisabledRoute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	etcdClient, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	upHost, upPort, upClose := startUpstreamMock(t)
	defer upClose()

	putClusterJSON(t, etcdClient, "disabled-be", fmt.Sprintf(`{
		"name": "disabled-be",
		"type": "roundrobin",
		"timeout": {"connect": 3, "send": 6, "read": 6},
		"nodes": [{"host": "%s", "port": %d, "weight": 100}]
	}`, upHost, upPort))

	// One enabled route, one disabled route
	putDomainJSON(t, etcdClient, "status-test", `{
		"name": "status-test",
		"hosts": ["status.test.local"],
		"routes": [
			{
				"name": "enabled",
				"uri": "/enabled/*",
				"status": 1,
				"clusters": [{"name": "disabled-be", "weight": 100}]
			},
			{
				"name": "disabled",
				"uri": "/disabled/*",
				"status": 0,
				"clusters": [{"name": "disabled-be", "weight": 100}]
			}
		]
	}`)

	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	proxyBase := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)

	// Enabled route → 200
	resp := proxyRequest(t, proxyBase, "GET", "/enabled/test", "status.test.local", nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Disabled route → 404
	resp = proxyRequest(t, proxyBase, "GET", "/disabled/test", "status.test.local", nil)
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
}

// ── Metrics Endpoint ────────────────────────────────────────────────

// TestE2E_Dataplane_MetricsEndpoint tests that the gateway exposes metrics.
func TestE2E_Dataplane_MetricsEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	ctx := context.Background()

	gwBin := buildGateway(t)

	_, etcdEndpoint, cleanupEtcd := startEtcd(t, ctx)
	defer cleanupEtcd()
	consulAddr, cleanupConsul := startConsul(t, ctx)
	defer cleanupConsul()

	tmpDir := t.TempDir()
	gwConfig := writeGatewayConfig(t, tmpDir, etcdEndpoint, consulAddr, false)
	proxyPort := freePort(t)
	adminPort := freePort(t)
	gw := startGatewayProc(t, gwBin, gwConfig,
		fmt.Sprintf("127.0.0.1:%d", proxyPort),
		fmt.Sprintf("127.0.0.1:%d", adminPort))
	defer gw.stop()
	gw.waitReady(t, 15*time.Second)

	metricsResp, err := http.Get("http://" + gw.adminAddr + "/metrics")
	require.NoError(t, err)
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	assert.Equal(t, 200, metricsResp.StatusCode)
	assert.True(t, len(metricsBody) > 0, "metrics should contain data")
}
