package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jizhuozhi/hermes/controller/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// startEtcd starts an etcd container and returns the client endpoint.
func startEtcd(t *testing.T, ctx context.Context) (string, func()) {
	t.Helper()
	req := testcontainers.ContainerRequest{
		Image:        "quay.io/coreos/etcd:v3.5.17",
		ExposedPorts: []string{"2379/tcp"},
		Env: map[string]string{
			"ETCD_ADVERTISE_CLIENT_URLS": "http://0.0.0.0:2379",
			"ETCD_LISTEN_CLIENT_URLS":    "http://0.0.0.0:2379",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("2379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	return "http://" + endpoint, func() { container.Terminate(ctx) }
}

// mockControlplane creates a test HTTP server that mimics the controlplane API.
type mockControlplane struct {
	mu       sync.Mutex
	domains  []json.RawMessage
	clusters []json.RawMessage
	changes  []ChangeEvent
	revision int64
}

func newMockControlplane() *mockControlplane {
	return &mockControlplane{revision: 0}
}

func (m *mockControlplane) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{
				"domains":  m.domains,
				"clusters": m.clusters,
			},
		})
	})

	mux.HandleFunc("GET /api/v1/config/revision", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"revision": m.revision})
	})

	mux.HandleFunc("GET /api/v1/config/watch", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		json.NewEncoder(w).Encode(WatchResponse{
			Events:   m.changes,
			Revision: m.revision,
			Total:    len(m.changes),
		})
		m.changes = nil // consumed
	})

	mux.HandleFunc("PUT /api/v1/status/controller", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("PUT /api/v1/status/instances", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	return mux
}

func (m *mockControlplane) addDomain(name string, data json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.domains = append(m.domains, data)
	m.revision++
	m.changes = append(m.changes, ChangeEvent{
		Revision: m.revision,
		Kind:     "domain",
		Name:     name,
		Action:   "create",
		Domain:   data,
	})
}

func (m *mockControlplane) addCluster(name string, data json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters = append(m.clusters, data)
	m.revision++
	m.changes = append(m.changes, ChangeEvent{
		Revision: m.revision,
		Kind:     "cluster",
		Name:     name,
		Action:   "create",
		Cluster:  data,
	})
}

func (m *mockControlplane) deleteDomain(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := m.domains[:0]
	for _, d := range m.domains {
		var h struct {
			Name string `json:"name"`
		}
		json.Unmarshal(d, &h)
		if h.Name != name {
			filtered = append(filtered, d)
		}
	}
	m.domains = filtered
	m.revision++
	m.changes = append(m.changes, ChangeEvent{
		Revision: m.revision,
		Kind:     "domain",
		Name:     name,
		Action:   "delete",
	})
}

func newTestController(t *testing.T, cpURL, etcdEndpoint string) *Controller {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	sugar := logger.Sugar()

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			URL:               cpURL,
			PollInterval:      1,
			ReconcileInterval: 60,
			Region:            "default",
		},
		Etcd: config.EtcdConfig{
			Endpoints:      []string{etcdEndpoint},
			DomainPrefix:   "/hermes/domains",
			ClusterPrefix:  "/hermes/clusters",
			InstancePrefix: "/hermes/instances",
			MetaPrefix:     "/hermes/meta",
		},
	}

	ctrl, err := New(cfg, sugar)
	require.NoError(t, err)
	return ctrl
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	domainData := json.RawMessage(`{"name":"test-domain","hosts":["api.example.com"],"routes":[]}`)
	clusterData := json.RawMessage(`{"name":"test-cluster","type":"roundrobin","nodes":[{"host":"10.0.0.1","port":8080,"weight":100}]}`)
	cp.addDomain("test-domain", domainData)
	cp.addCluster("test-cluster", clusterData)

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	err := ctrl.Reconcile(ctx)
	require.NoError(t, err)

	// Verify data landed in etcd
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, err := etcdClient.Get(ctx, "/hermes/domains/test-domain")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	assert.Contains(t, string(resp.Kvs[0].Value), "test-domain")

	resp, err = etcdClient.Get(ctx, "/hermes/clusters/test-cluster")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	assert.Contains(t, string(resp.Kvs[0].Value), "test-cluster")
}

func TestReconcile_DeletesDirtyKeys(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	// Pre-populate etcd with a key that should not exist
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	_, err = etcdClient.Put(ctx, "/hermes/domains/dirty-domain", `{"name":"dirty-domain","hosts":["stale.com"]}`)
	require.NoError(t, err)

	cp := newMockControlplane() // empty config — should delete the dirty key
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	err = ctrl.Reconcile(ctx)
	require.NoError(t, err)

	resp, err := etcdClient.Get(ctx, "/hermes/domains/dirty-domain")
	require.NoError(t, err)
	assert.Empty(t, resp.Kvs, "dirty key should have been deleted")
}

func TestApplyEvent_CreateAndDelete(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	// Apply create event
	domainData := json.RawMessage(`{"name":"event-domain","hosts":["ev.example.com"]}`)
	err := ctrl.applyEvent(ctx, ChangeEvent{
		Kind:   "domain",
		Name:   "event-domain",
		Action: "create",
		Domain: domainData,
	})
	require.NoError(t, err)

	// Verify in etcd
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, err := etcdClient.Get(ctx, "/hermes/domains/event-domain")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)

	// Apply delete event
	err = ctrl.applyEvent(ctx, ChangeEvent{
		Kind:   "domain",
		Name:   "event-domain",
		Action: "delete",
	})
	require.NoError(t, err)

	resp, err = etcdClient.Get(ctx, "/hermes/domains/event-domain")
	require.NoError(t, err)
	assert.Empty(t, resp.Kvs, "should have been deleted")
}

func TestPollOnce(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	// Initial reconcile
	require.NoError(t, ctrl.Reconcile(ctx))

	// Push a change event
	clusterData := json.RawMessage(`{"name":"poll-cluster","type":"random","nodes":[{"host":"10.0.0.5","port":9090,"weight":100}]}`)
	cp.addCluster("poll-cluster", clusterData)

	// Poll
	ctrl.pollOnce(ctx)

	// Verify cluster in etcd
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, err := etcdClient.Get(ctx, "/hermes/clusters/poll-cluster")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	assert.Contains(t, string(resp.Kvs[0].Value), "poll-cluster")
}

func TestPublishRevisionToEtcd(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	ctrl.SetRevision(42)
	ctrl.publishRevisionToEtcd(ctx)

	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, err := etcdClient.Get(ctx, "/hermes/meta/config_revision")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	assert.Equal(t, "42", string(resp.Kvs[0].Value))
}

func TestFetchRevision(t *testing.T) {
	cp := newMockControlplane()
	cp.mu.Lock()
	cp.revision = 99
	cp.mu.Unlock()

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	rev, err := ctrl.fetchRevision(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(99), rev)
}

func TestExtractName(t *testing.T) {
	assert.Equal(t, "foo", extractName(json.RawMessage(`{"name":"foo","hosts":["a.com"]}`)))
	assert.Equal(t, "", extractName(json.RawMessage(`{"hosts":["a.com"]}`)))
	assert.Equal(t, "", extractName(json.RawMessage(`invalid`)))
}

func TestCanonicalJSON(t *testing.T) {
	// Different JSON formatting but same content should produce same output
	a := canonicalJSON(json.RawMessage(`{  "name": "a",  "hosts":  ["b.com"]  }`))
	b := canonicalJSON(json.RawMessage(`{"name":"a","hosts":["b.com"]}`))
	assert.Equal(t, a, b)
}

func TestDiffKeys(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	// Setup: put a stale key and a current key
	_, err = etcdClient.Put(ctx, "/hermes/domains/stale", `{"name":"stale"}`)
	require.NoError(t, err)
	_, err = etcdClient.Put(ctx, "/hermes/domains/current", `{"name":"current"}`)
	require.NoError(t, err)

	desired := map[string]string{
		"/hermes/domains/current": `{"name":"current"}`,
		"/hermes/domains/new":     `{"name":"new"}`,
	}

	actual, err := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	require.NoError(t, err)

	ops := ctrl.diffKeys(desired, actual)

	var putKeys, deleteKeys []string
	for _, op := range ops {
		switch op.opType {
		case "put":
			putKeys = append(putKeys, op.key)
		case "delete":
			deleteKeys = append(deleteKeys, op.key)
		}
	}

	assert.Contains(t, putKeys, "/hermes/domains/new")
	assert.Contains(t, deleteKeys, "/hermes/domains/stale")
	// "current" should NOT be in puts since value matches
	for _, k := range putKeys {
		assert.NotEqual(t, "/hermes/domains/current", k)
	}
}

func TestApplyEvent_UnknownKind(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	// Should not error, just skip
	err := ctrl.applyEvent(ctx, ChangeEvent{
		Kind:   "unknown",
		Name:   "foo",
		Action: "create",
	})
	assert.NoError(t, err)
}

func TestApplyEvent_NilData(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	// Create event with nil domain data — should skip
	err := ctrl.applyEvent(ctx, ChangeEvent{
		Kind:   "domain",
		Name:   "nil-domain",
		Action: "create",
		Domain: nil,
	})
	assert.NoError(t, err)

	// Verify nothing was written
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, _ := etcdClient.Get(ctx, "/hermes/domains/nil-domain")
	assert.Empty(t, resp.Kvs)
}

func TestRevisionAtomicOps(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	assert.Equal(t, int64(0), ctrl.GetRevision())
	ctrl.SetRevision(100)
	assert.Equal(t, int64(100), ctrl.GetRevision())
	ctrl.SetRevision(200)
	assert.Equal(t, int64(200), ctrl.GetRevision())
}

func TestReconcileMultipleDomains(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("domain-%d", i)
		data := json.RawMessage(fmt.Sprintf(`{"name":"%s","hosts":["host-%d.example.com"]}`, name, i))
		cp.addDomain(name, data)
	}

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	err := ctrl.Reconcile(ctx)
	require.NoError(t, err)

	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	resp, err := etcdClient.Get(ctx, "/hermes/domains/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 10, "should have all 10 domains in etcd")
}

// TestFetchChanges ensures the watch response is correctly parsed.
func TestFetchChanges(t *testing.T) {
	cp := newMockControlplane()
	domainData := json.RawMessage(`{"name":"watch-domain","hosts":["watch.example.com"]}`)
	cp.addDomain("watch-domain", domainData)

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	events, rev, err := ctrl.fetchChanges(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rev)
	require.Len(t, events, 1)
	assert.Equal(t, "domain", events[0].Kind)
	assert.Equal(t, "watch-domain", events[0].Name)
	assert.Equal(t, "create", events[0].Action)

	// Second fetch should return no events (consumed)
	events2, _, err := ctrl.fetchChanges(ctx)
	require.NoError(t, err)
	assert.Empty(t, events2)
}

func TestIsLeaderFlag(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	assert.False(t, ctrl.IsLeader())
	ctrl.SetLeader(true)
	assert.True(t, ctrl.IsLeader())
	ctrl.SetLeader(false)
	assert.False(t, ctrl.IsLeader())
}

func TestHeartbeatReportsLeaderStatus(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	var mu sync.Mutex
	var lastReport controllerReport

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/status/controller", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &lastReport)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	defer ctrl.Close()

	ctrl.SetLeader(true)
	err := ctrl.reportControllerStatus(ctx, "running")
	require.NoError(t, err)

	mu.Lock()
	assert.True(t, lastReport.IsLeader)
	assert.Equal(t, "running", lastReport.Status)
	mu.Unlock()

	ctrl.SetLeader(false)
	err = ctrl.reportControllerStatus(ctx, "running")
	require.NoError(t, err)

	mu.Lock()
	assert.False(t, lastReport.IsLeader)
	mu.Unlock()
}

func TestRunWithElection_SingleInstance(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	domainData := json.RawMessage(`{"name":"election-domain","hosts":["elect.example.com"]}`)
	cp.addDomain("election-domain", domainData)

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl := newTestController(t, srv.URL, etcdEndpoint)
	ctrl.cfg.Election.Enabled = true
	ctrl.cfg.Election.Prefix = "/hermes/test-election"
	ctrl.cfg.Election.LeaseTTL = 5
	defer ctrl.Close()

	runCtx, cancel := context.WithCancel(ctx)

	done := make(chan error, 1)
	go func() {
		done <- ctrl.RunWithElection(runCtx)
	}()

	// Wait for leader to be elected and reconcile to complete
	require.Eventually(t, func() bool {
		return ctrl.IsLeader()
	}, 10*time.Second, 100*time.Millisecond, "should become leader")

	// Verify data landed in etcd
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdEndpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer etcdClient.Close()

	require.Eventually(t, func() bool {
		resp, err := etcdClient.Get(ctx, "/hermes/domains/election-domain")
		return err == nil && len(resp.Kvs) == 1
	}, 10*time.Second, 200*time.Millisecond, "domain should be synced to etcd")

	cancel()
	require.NoError(t, <-done)
}

func TestRunWithElection_LeadershipHandover(t *testing.T) {
	ctx := context.Background()
	etcdEndpoint, cleanup := startEtcd(t, ctx)
	defer cleanup()

	cp := newMockControlplane()
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	ctrl1 := newTestController(t, srv.URL, etcdEndpoint)
	ctrl1.cfg.Election.Enabled = true
	ctrl1.cfg.Election.Prefix = "/hermes/handover-election"
	ctrl1.cfg.Election.LeaseTTL = 5
	ctrl1.hostname = "controller-1"

	ctrl2 := newTestController(t, srv.URL, etcdEndpoint)
	ctrl2.cfg.Election.Enabled = true
	ctrl2.cfg.Election.Prefix = "/hermes/handover-election"
	ctrl2.cfg.Election.LeaseTTL = 5
	ctrl2.hostname = "controller-2"

	ctx1, cancel1 := context.WithCancel(ctx)
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()

	done1 := make(chan error, 1)
	go func() {
		done1 <- ctrl1.RunWithElection(ctx1)
	}()

	done2 := make(chan error, 1)
	go func() {
		done2 <- ctrl2.RunWithElection(ctx2)
	}()

	// Wait for ctrl1 to become leader
	require.Eventually(t, func() bool {
		return ctrl1.IsLeader() || ctrl2.IsLeader()
	}, 10*time.Second, 100*time.Millisecond, "one controller should become leader")

	// Stop the leader
	if ctrl1.IsLeader() {
		cancel1()
		<-done1
		ctrl1.Close()

		require.Eventually(t, func() bool {
			return ctrl2.IsLeader()
		}, 15*time.Second, 200*time.Millisecond, "ctrl2 should take over leadership")
		cancel2()
		<-done2
		ctrl2.Close()
	} else {
		cancel2()
		<-done2
		ctrl2.Close()

		require.Eventually(t, func() bool {
			return ctrl1.IsLeader()
		}, 15*time.Second, 200*time.Millisecond, "ctrl1 should take over leadership")
		cancel1()
		<-done1
		ctrl1.Close()
	}
}
