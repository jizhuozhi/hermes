// Package e2e implements end-to-end integration tests for the Hermes pipeline.
//
// The test exercises the full data flow:
//
//	Controlplane API (HTTP) → Controller binary (sync) → etcd (verify)
//	Controlplane API → Controller → etcd → Gateway binary (proxy)
//
// All components are compiled and run as real binaries (pure black-box).
// Infrastructure (PostgreSQL + etcd + Consul) is started via testcontainers.
package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ══════════════════════════════════════════════════════════
//  Infrastructure Helpers
// ══════════════════════════════════════════════════════════

func startEtcd(t *testing.T, ctx context.Context) (*clientv3.Client, string, func()) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "quay.io/coreos/etcd:v3.5.17",
			ExposedPorts: []string{"2379/tcp"},
			Env: map[string]string{
				"ETCD_ADVERTISE_CLIENT_URLS":  "http://0.0.0.0:2379",
				"ETCD_LISTEN_CLIENT_URLS":     "http://0.0.0.0:2379",
			},
			WaitingFor: wait.ForLog("ready to serve client requests").
				WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "2379")
	require.NoError(t, err)

	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	return client, endpoint, func() {
		client.Close()
		container.Terminate(ctx)
	}
}

func startConsul(t *testing.T, ctx context.Context) (string, func()) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "hashicorp/consul:1.16",
			ExposedPorts: []string{"8500/tcp"},
			Cmd:          []string{"agent", "-dev", "-client=0.0.0.0"},
			WaitingFor: wait.ForHTTP("/v1/status/leader").
				WithPort("8500/tcp").
				WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "8500")
	require.NoError(t, err)
	addr := fmt.Sprintf("http://%s:%s", host, port.Port())
	return addr, func() { container.Terminate(ctx) }
}

// ══════════════════════════════════════════════════════════
//  PostgreSQL Testcontainer
// ══════════════════════════════════════════════════════════

func startPostgres(t *testing.T, ctx context.Context) (dsn string, cleanup func()) {
	t.Helper()
	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("hermes_e2e"),
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

	return connStr, func() { pgContainer.Terminate(ctx) }
}

// ══════════════════════════════════════════════════════════
//  Server Binary Builder & Process Helper
// ══════════════════════════════════════════════════════════

func buildServer(t *testing.T) string {
	t.Helper()
	root := projectRoot(t)
	srvDir := filepath.Join(root, "server")
	bin := filepath.Join(srvDir, "hermes-server")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/server")
	cmd.Dir = srvDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "failed to build server")
	require.FileExists(t, bin)
	return bin
}

type serverOpts struct {
	pgDSN             string
	listenAddr        string // e.g. "127.0.0.1:19080"
	oidcEnabled       bool
	oidcIssuer        string
	oidcClientID      string
	oidcClientSecret  string
	initialAdminUsers string
	// Builtin auth mode
	builtinAuth       bool
	builtinAdminEmail string
	builtinAdminPass  string
}

type serverProc struct {
	cmd        *exec.Cmd
	baseURL    string
	configPath string
}

func startServerProc(t *testing.T, srvBin string, opts serverOpts) *serverProc {
	t.Helper()

	if opts.listenAddr == "" {
		opts.listenAddr = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}

	cfgYAML := fmt.Sprintf(`server:
  listen: %q

postgres:
  dsn: %q
`, opts.listenAddr, opts.pgDSN)

	if opts.oidcEnabled {
		cfgYAML += fmt.Sprintf(`
oidc:
  enabled: true
  issuer: %q
  client_id: %q
  client_secret: %q
  initial_admin_users: %q
`, opts.oidcIssuer, opts.oidcClientID, opts.oidcClientSecret, opts.initialAdminUsers)
	}

	if opts.builtinAuth {
		cfgYAML += fmt.Sprintf(`
auth_mode: "builtin"
builtin_auth:
  initial_admin_email: %q
  initial_admin_password: %q
`, opts.builtinAdminEmail, opts.builtinAdminPass)
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgYAML), 0644))

	cmd := exec.Command(srvBin, "-config", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	baseURL := "http://" + opts.listenAddr
	sp := &serverProc{cmd: cmd, baseURL: baseURL, configPath: cfgPath}

	// Wait for the server to be ready (poll /api/v1/config/revision).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/auth/config")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return sp
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	sp.stop()
	t.Fatalf("server did not become ready within 30s at %s", baseURL)
	return nil
}

func (s *serverProc) stop() {
	if s.cmd.Process != nil {
		s.cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			s.cmd.Process.Kill()
		}
	}
}

// ══════════════════════════════════════════════════════════
//  Config Data Types
// ══════════════════════════════════════════════════════════

type domainConfig struct {
	Name   string        `json:"name"`
	Hosts  []string      `json:"hosts"`
	Routes []routeConfig `json:"routes"`
}

type routeConfig struct {
	Name     string            `json:"name"`
	URI      string            `json:"uri"`
	Clusters []weightedCluster `json:"clusters"`
	Status   int               `json:"status"`
}

type weightedCluster struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}

type clusterConfig struct {
	Name    string         `json:"name"`
	LBType  string         `json:"type"`
	Timeout timeoutConfig  `json:"timeout"`
	Nodes   []upstreamNode `json:"nodes"`
}

type timeoutConfig struct {
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Read    float64 `json:"read"`
}

type upstreamNode struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Weight int    `json:"weight"`
}

type gatewayConfig struct {
	Domains  []domainConfig  `json:"domains"`
	Clusters []clusterConfig `json:"clusters"`
}

// ══════════════════════════════════════════════════════════
//  API Client Helpers
// ══════════════════════════════════════════════════════════

func apiPost(t *testing.T, base, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	return resp
}

func apiPut(t *testing.T, base, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("PUT", base+path, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func apiGet(t *testing.T, base, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(base + path)
	require.NoError(t, err)
	return resp
}

func apiDelete(t *testing.T, base, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("DELETE", base+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m), "body: %s", string(b))
	return m
}

// ══════════════════════════════════════════════════════════
//  HMAC Auth Helpers
// ══════════════════════════════════════════════════════════

func hmacRequest(t *testing.T, method, url, accessKey, secretKey string, body any) *http.Response {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}

	bodyHash := sha256Hex(bodyBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	path := extractPath(url)

	stringToSign := method + "\n" + path + "\n" + timestamp + "\n" + bodyHash
	signature := computeHMACSHA256(secretKey, stringToSign)

	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("HMAC-SHA256 Credential=%s,Signature=%s", accessKey, signature))
	req.Header.Set("X-Hermes-Timestamp", timestamp)
	req.Header.Set("X-Hermes-Body-SHA256", bodyHash)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func hmacRequestWithNS(t *testing.T, method, url, accessKey, secretKey, ns string, body any) *http.Response {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}

	bodyHash := sha256Hex(bodyBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	path := extractPath(url)

	stringToSign := method + "\n" + path + "\n" + timestamp + "\n" + bodyHash
	signature := computeHMACSHA256(secretKey, stringToSign)

	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("HMAC-SHA256 Credential=%s,Signature=%s", accessKey, signature))
	req.Header.Set("X-Hermes-Timestamp", timestamp)
	req.Header.Set("X-Hermes-Body-SHA256", bodyHash)
	req.Header.Set("X-Hermes-Namespace", ns)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func computeHMACSHA256(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func extractPath(rawURL string) string {
	idx := 0
	slashes := 0
	for i, c := range rawURL {
		if c == '/' {
			slashes++
			if slashes == 3 {
				idx = i
				break
			}
		}
	}
	if idx == 0 {
		return "/"
	}
	path := rawURL[idx:]
	if qIdx := strings.Index(path, "?"); qIdx != -1 {
		path = path[:qIdx]
	}
	return path
}

// ══════════════════════════════════════════════════════════
//  Binary Builders
// ══════════════════════════════════════════════════════════

func projectRoot(t *testing.T) string {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(f))
}

func buildController(t *testing.T) string {
	t.Helper()
	root := projectRoot(t)
	ctrlDir := filepath.Join(root, "controller")
	bin := filepath.Join(ctrlDir, "hermes-controller")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/controller")
	cmd.Dir = ctrlDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "failed to build controller")
	require.FileExists(t, bin)
	return bin
}

func buildGateway(t *testing.T) string {
	t.Helper()
	root := projectRoot(t)
	gwDir := filepath.Join(root, "gateway")
	cmd := exec.Command("cargo", "build", "--manifest-path", filepath.Join(gwDir, "Cargo.toml"))
	cmd.Dir = gwDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "failed to build gateway")
	bin := filepath.Join(gwDir, "target", "debug", "hermes-gateway")
	require.FileExists(t, bin)
	return bin
}

// ══════════════════════════════════════════════════════════
//  Controller Process Helper
// ══════════════════════════════════════════════════════════

type controllerProc struct {
	cmd        *exec.Cmd
	configPath string
}

type controllerOpts struct {
	cpURL             string
	etcdEndpoint      string
	domainPrefix      string
	clusterPrefix     string
	metaPrefix        string
	instancePrefix    string
	namespace         string
	accessKey         string
	secretKey         string
	pollInterval      int
	reconcileInterval int
	electionEnabled   bool
	electionPrefix    string
	electionLeaseTTL  int
}

func startControllerProc(t *testing.T, ctrlBin string, opts controllerOpts) *controllerProc {
	t.Helper()

	if opts.domainPrefix == "" {
		opts.domainPrefix = "/hermes/domains"
	}
	if opts.clusterPrefix == "" {
		opts.clusterPrefix = "/hermes/clusters"
	}
	if opts.metaPrefix == "" {
		opts.metaPrefix = "/hermes/meta"
	}
	if opts.instancePrefix == "" {
		opts.instancePrefix = "/hermes/instances"
	}
	if opts.namespace == "" {
		opts.namespace = "default"
	}
	if opts.pollInterval == 0 {
		opts.pollInterval = 1
	}
	if opts.reconcileInterval == 0 {
		opts.reconcileInterval = 3
	}

	cfgYAML := fmt.Sprintf(`controlplane:
  url: %q
  poll_interval: %d
  reconcile_interval: %d
  namespace: %q

etcd:
  endpoints:
    - %q
  domain_prefix: %q
  cluster_prefix: %q
  instance_prefix: %q
  meta_prefix: %q
`,
		opts.cpURL,
		opts.pollInterval,
		opts.reconcileInterval,
		opts.namespace,
		opts.etcdEndpoint,
		opts.domainPrefix,
		opts.clusterPrefix,
		opts.instancePrefix,
		opts.metaPrefix,
	)

	if opts.accessKey != "" && opts.secretKey != "" {
		cfgYAML += fmt.Sprintf(`
auth:
  access_key: %q
  secret_key: %q
`, opts.accessKey, opts.secretKey)
	}

	if opts.electionEnabled {
		prefix := opts.electionPrefix
		if prefix == "" {
			prefix = "/hermes/election"
		}
		ttl := opts.electionLeaseTTL
		if ttl == 0 {
			ttl = 5
		}
		cfgYAML += fmt.Sprintf(`
election:
  enabled: true
  prefix: %q
  lease_ttl: %d
`, prefix, ttl)
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgYAML), 0644))

	cmd := exec.Command(ctrlBin, "-config", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	return &controllerProc{cmd: cmd, configPath: cfgPath}
}

func (c *controllerProc) stop() {
	if c.cmd.Process != nil {
		c.cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			c.cmd.Process.Kill()
		}
	}
}

// ══════════════════════════════════════════════════════════
//  Gateway Process Helper
// ══════════════════════════════════════════════════════════

type gatewayProc struct {
	cmd       *exec.Cmd
	proxyAddr string
	adminAddr string
	configDir string
}

func startGatewayProc(t *testing.T, gwBin, configPath, listenAddr, adminAddr string) *gatewayProc {
	t.Helper()
	cmd := exec.Command(gwBin, "-c", configPath, "-l", listenAddr, "--admin-listen", adminAddr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "RUST_LOG=info")
	require.NoError(t, cmd.Start())
	return &gatewayProc{cmd: cmd, proxyAddr: listenAddr, adminAddr: adminAddr}
}

func (g *gatewayProc) stop() {
	if g.cmd.Process != nil {
		g.cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- g.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			g.cmd.Process.Kill()
		}
	}
}

func (g *gatewayProc) waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + g.adminAddr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("gateway did not become healthy within %v", timeout)
}

func writeGatewayConfig(t *testing.T, dir string, etcdEndpoint, consulAddr string, instanceRegistryEnabled bool) string {
	t.Helper()
	cfg := fmt.Sprintf(`[consul]
address = "%s"
poll_interval_secs = 2

[etcd]
endpoints = ["%s"]
domain_prefix = "/hermes/domains"
cluster_prefix = "/hermes/clusters"
meta_prefix = "/hermes/meta"

[instance_registry]
enabled = %t
prefix = "/hermes/instances"
lease_ttl_secs = 10
`, consulAddr, etcdEndpoint, instanceRegistryEnabled)

	path := filepath.Join(dir, "gateway-test.toml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0644))
	return path
}

// ══════════════════════════════════════════════════════════
//  Upstream Mock
// ══════════════════════════════════════════════════════════

func startUpstreamMock(t *testing.T) (string, int, func()) {
	t.Helper()
	var counter atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := counter.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Id", "mock")
		json.NewEncoder(w).Encode(map[string]any{
			"message":    "hello from upstream",
			"path":       r.URL.Path,
			"host":       r.Host,
			"method":     r.Method,
			"request_id": n,
		})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() { srv.Close() }
}

// ══════════════════════════════════════════════════════════
//  Consul Helpers
// ══════════════════════════════════════════════════════════

func consulRegisterService(t *testing.T, consulAddr, name, host string, port int, meta map[string]string) {
	t.Helper()
	body := map[string]any{
		"ID":      fmt.Sprintf("%s-%s-%d", name, host, port),
		"Name":    name,
		"Address": host,
		"Port":    port,
		"Meta":    meta,
	}
	resp := apiPut(t, consulAddr, "/v1/agent/service/register", body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "consul register failed")
	resp.Body.Close()
}

func consulDeregisterService(t *testing.T, consulAddr, serviceID string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", consulAddr+"/v1/agent/service/deregister/"+serviceID, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
}

// ══════════════════════════════════════════════════════════
//  Networking Helpers
// ══════════════════════════════════════════════════════════

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// ══════════════════════════════════════════════════════════
//  etcd Wait Helpers
// ══════════════════════════════════════════════════════════

func waitForEtcdCount(t *testing.T, etcdClient *clientv3.Client, prefix string, min int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := etcdClient.Get(context.Background(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
		if err == nil && int(resp.Count) >= min {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	resp, _ := etcdClient.Get(context.Background(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
	t.Fatalf("timeout waiting for etcd prefix %q to have >= %d keys (got %d)", prefix, min, resp.Count)
}

func waitForEtcdCountExact(t *testing.T, etcdClient *clientv3.Client, prefix string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := etcdClient.Get(context.Background(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
		if err == nil && int(resp.Count) == count {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	resp, _ := etcdClient.Get(context.Background(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
	t.Fatalf("timeout waiting for etcd prefix %q to have exactly %d keys (got %d)", prefix, count, resp.Count)
}

func waitForEtcdKey(t *testing.T, etcdClient *clientv3.Client, key string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := etcdClient.Get(context.Background(), key)
		if err == nil && resp.Count > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for etcd key %q to exist", key)
}

func waitForEtcdKeyGone(t *testing.T, etcdClient *clientv3.Client, key string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := etcdClient.Get(context.Background(), key)
		if err == nil && resp.Count == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for etcd key %q to be deleted", key)
}

// extractKeyNames returns a set of the last path segment of each etcd key.
func extractKeyNames(resp *clientv3.GetResponse) map[string]bool {
	names := make(map[string]bool, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		parts := strings.Split(key, "/")
		names[parts[len(parts)-1]] = true
	}
	return names
}

// ══════════════════════════════════════════════════════════
//  Mock OIDC Provider
// ══════════════════════════════════════════════════════════

// mockOIDCProvider is a lightweight OIDC IdP that implements:
//   - GET  /.well-known/openid-configuration   (OIDC Discovery)
//   - GET  /authorize                           (redirect with code)
//   - POST /token                               (code→JWT / refresh→JWT)
//   - GET  /jwks                                (RSA public key)
type mockOIDCProvider struct {
	Server       *httptest.Server
	PrivateKey   *rsa.PrivateKey
	Kid          string
	ClientID     string
	ClientSecret string

	// pendingCodes maps authorization codes to the claims they represent.
	pendingCodes map[string]*mockOIDCUser

	// refreshTokens maps refresh tokens to the claims they represent.
	refreshTokens map[string]*mockOIDCUser
}

type mockOIDCUser struct {
	Sub               string
	PreferredUsername  string
	Email             string
	Name              string
	Groups            []string
}

// startMockOIDCProvider creates a mock OIDC provider with a fresh RSA key.
func startMockOIDCProvider(t *testing.T) *mockOIDCProvider {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	p := &mockOIDCProvider{
		PrivateKey:    privKey,
		Kid:           "test-kid-1",
		ClientID:      "hermes-test",
		ClientSecret:  "test-secret",
		pendingCodes:  make(map[string]*mockOIDCUser),
		refreshTokens: make(map[string]*mockOIDCUser),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.handleDiscovery)
	mux.HandleFunc("GET /authorize", p.handleAuthorize)
	mux.HandleFunc("POST /token", p.handleToken)
	mux.HandleFunc("GET /jwks", p.handleJWKS)

	p.Server = httptest.NewServer(mux)
	return p
}

func (p *mockOIDCProvider) Close() {
	p.Server.Close()
}

func (p *mockOIDCProvider) IssuerURL() string {
	return p.Server.URL
}

// RegisterCode pre-registers an authorization code for a user.
func (p *mockOIDCProvider) RegisterCode(code string, user *mockOIDCUser) {
	p.pendingCodes[code] = user
}

func (p *mockOIDCProvider) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	issuer := p.Server.URL
	json.NewEncoder(w).Encode(map[string]string{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + "/authorize",
		"token_endpoint":         issuer + "/token",
		"jwks_uri":               issuer + "/jwks",
	})
}

func (p *mockOIDCProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	// In a real flow the user would authenticate; here we just redirect with a fixed code.
	redirectURI := r.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		http.Error(w, "redirect_uri required", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, redirectURI+"?code=mock-auth-code", http.StatusFound)
}

func (p *mockOIDCProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	grantType := r.FormValue("grant_type")

	var user *mockOIDCUser

	switch grantType {
	case "authorization_code":
		code := r.FormValue("code")
		u, ok := p.pendingCodes[code]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(p.pendingCodes, code)
		user = u

	case "refresh_token":
		rt := r.FormValue("refresh_token")
		u, ok := p.refreshTokens[rt]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		user = u

	default:
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unsupported_grant_type"})
		return
	}

	// Sign an access token.
	accessToken := p.signJWT(user, time.Now().Add(1*time.Hour))
	refreshToken := "rt-" + user.Sub + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	p.refreshTokens[refreshToken] = user

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

func (p *mockOIDCProvider) handleJWKS(w http.ResponseWriter, r *http.Request) {
	pub := &p.PrivateKey.PublicKey
	nBytes := pub.N.Bytes()
	eBytes := big.NewInt(int64(pub.E)).Bytes()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"alg": "RS256",
				"kid": p.Kid,
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(nBytes),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	})
}

// signJWT creates a signed RS256 JWT with the given claims.
func (p *mockOIDCProvider) signJWT(user *mockOIDCUser, expiry time.Time) string {
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.Kid}
	claims := map[string]any{
		"iss":                p.Server.URL,
		"sub":                user.Sub,
		"aud":                p.ClientID,
		"azp":                p.ClientID,
		"exp":                expiry.Unix(),
		"iat":                time.Now().Unix(),
		"preferred_username": user.PreferredUsername,
		"email":              user.Email,
		"name":               user.Name,
	}
	if len(user.Groups) > 0 {
		claims["groups"] = user.Groups
	}

	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerB64 + "." + claimsB64
	hash := sha256.Sum256([]byte(signingInput))
	sigBytes, _ := rsa.SignPKCS1v15(rand.Reader, p.PrivateKey, crypto.SHA256, hash[:])
	sigB64 := base64.RawURLEncoding.EncodeToString(sigBytes)

	return signingInput + "." + sigB64
}

// signExpiredJWT creates a JWT that has already expired.
func (p *mockOIDCProvider) signExpiredJWT(user *mockOIDCUser) string {
	return p.signJWT(user, time.Now().Add(-1*time.Hour))
}

// signJWTWrongKey creates a JWT signed with a different RSA key (should fail verification).
func (p *mockOIDCProvider) signJWTWrongKey(user *mockOIDCUser) string {
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.Kid}
	claims := map[string]any{
		"iss": p.Server.URL, "sub": user.Sub, "aud": p.ClientID,
		"exp": time.Now().Add(1 * time.Hour).Unix(),
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64
	hash := sha256.Sum256([]byte(signingInput))
	sigBytes, _ := rsa.SignPKCS1v15(rand.Reader, wrongKey, crypto.SHA256, hash[:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sigBytes)
}

// bearerRequest makes an HTTP request with a Bearer token.
func bearerRequest(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	// Don't follow redirects automatically for login flow tests.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}
