package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/model"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── Mock Store ──────────────────────────────────────

// mockStore implements store.Store for handler unit tests.
// Only the methods used by tested handlers are implemented;
// the rest panic with "not implemented".
type mockStore struct {
	domains    map[string]map[string]*model.DomainConfig // ns → name → config
	clusters   map[string]map[string]*model.ClusterConfig
	creds      map[string][]store.APICredential
	credsByAK  map[string]*store.APICredential
	dashboards map[string][]store.GrafanaDashboard
	instances  map[string][]store.GatewayInstanceStatus
	ctrl       map[string]*store.ControllerStatus
	auditLog   []store.AuditEntry
	changes    []store.ChangeEvent
	revision   int64
	nextID     int64
}

func newMockStore() *mockStore {
	return &mockStore{
		domains:    make(map[string]map[string]*model.DomainConfig),
		clusters:   make(map[string]map[string]*model.ClusterConfig),
		creds:      make(map[string][]store.APICredential),
		credsByAK:  make(map[string]*store.APICredential),
		dashboards: make(map[string][]store.GrafanaDashboard),
		instances:  make(map[string][]store.GatewayInstanceStatus),
		ctrl:       make(map[string]*store.ControllerStatus),
		nextID:     1,
	}
}

func (m *mockStore) Close() {}

func (m *mockStore) ListDomains(_ context.Context, ns string) ([]model.DomainConfig, error) {
	var result []model.DomainConfig
	for _, d := range m.domains[ns] {
		result = append(result, *d)
	}
	return result, nil
}

func (m *mockStore) GetDomain(_ context.Context, ns, name string) (*model.DomainConfig, error) {
	if nsm, ok := m.domains[ns]; ok {
		return nsm[name], nil
	}
	return nil, nil
}

func (m *mockStore) PutDomain(_ context.Context, ns string, d *model.DomainConfig, action, operator string) (int64, error) {
	if m.domains[ns] == nil {
		m.domains[ns] = make(map[string]*model.DomainConfig)
	}
	m.domains[ns][d.Name] = d
	m.revision++
	m.changes = append(m.changes, store.ChangeEvent{Revision: m.revision, Kind: "domain", Name: d.Name, Action: action, Domain: d})
	m.auditLog = append(m.auditLog, store.AuditEntry{Revision: m.revision, Kind: "domain", Name: d.Name, Action: action, Operator: operator, Timestamp: time.Now()})
	return m.revision, nil
}

func (m *mockStore) DeleteDomain(_ context.Context, ns, name, operator string) (int64, error) {
	if nsm, ok := m.domains[ns]; ok {
		if _, exists := nsm[name]; exists {
			delete(nsm, name)
			m.revision++
			return m.revision, nil
		}
	}
	return 0, &notFoundError{name}
}

func (m *mockStore) ListClusters(_ context.Context, ns string) ([]model.ClusterConfig, error) {
	var result []model.ClusterConfig
	for _, c := range m.clusters[ns] {
		result = append(result, *c)
	}
	return result, nil
}

func (m *mockStore) GetCluster(_ context.Context, ns, name string) (*model.ClusterConfig, error) {
	if nsm, ok := m.clusters[ns]; ok {
		return nsm[name], nil
	}
	return nil, nil
}

func (m *mockStore) PutCluster(_ context.Context, ns string, c *model.ClusterConfig, action, operator string) (int64, error) {
	if m.clusters[ns] == nil {
		m.clusters[ns] = make(map[string]*model.ClusterConfig)
	}
	m.clusters[ns][c.Name] = c
	m.revision++
	return m.revision, nil
}

func (m *mockStore) DeleteCluster(_ context.Context, ns, name, operator string) (int64, error) {
	if nsm, ok := m.clusters[ns]; ok {
		if _, exists := nsm[name]; exists {
			delete(nsm, name)
			m.revision++
			return m.revision, nil
		}
	}
	return 0, &notFoundError{name}
}

func (m *mockStore) PutAllConfig(_ context.Context, ns string, domains []model.DomainConfig, clusters []model.ClusterConfig, operator string) (int64, error) {
	m.domains[ns] = make(map[string]*model.DomainConfig)
	for i := range domains {
		m.domains[ns][domains[i].Name] = &domains[i]
	}
	m.clusters[ns] = make(map[string]*model.ClusterConfig)
	for i := range clusters {
		m.clusters[ns][clusters[i].Name] = &clusters[i]
	}
	m.revision++
	return m.revision, nil
}

func (m *mockStore) GetConfig(_ context.Context, ns string) (*model.GatewayConfig, error) {
	cfg := &model.GatewayConfig{}
	for _, d := range m.domains[ns] {
		cfg.Domains = append(cfg.Domains, *d)
	}
	for _, c := range m.clusters[ns] {
		cfg.Clusters = append(cfg.Clusters, *c)
	}
	return cfg, nil
}

func (m *mockStore) GetDomainHistory(_ context.Context, ns, name string) ([]store.HistoryEntry, error) {
	return nil, nil
}
func (m *mockStore) GetDomainVersion(_ context.Context, ns, name string, version int64) (*store.HistoryEntry, error) {
	return nil, nil
}
func (m *mockStore) RollbackDomain(_ context.Context, ns, name string, version int64, operator string) (int64, error) {
	m.revision++
	return m.revision, nil
}
func (m *mockStore) GetClusterHistory(_ context.Context, ns, name string) ([]store.HistoryEntry, error) {
	return nil, nil
}
func (m *mockStore) GetClusterVersion(_ context.Context, ns, name string, version int64) (*store.HistoryEntry, error) {
	return nil, nil
}
func (m *mockStore) RollbackCluster(_ context.Context, ns, name string, version int64, operator string) (int64, error) {
	m.revision++
	return m.revision, nil
}

func (m *mockStore) ListAuditLog(_ context.Context, ns string, limit, offset int) ([]store.AuditEntry, int64, error) {
	return m.auditLog, int64(len(m.auditLog)), nil
}
func (m *mockStore) InsertAuditLog(_ context.Context, ns, kind, name, action, operator string) error {
	m.auditLog = append(m.auditLog, store.AuditEntry{Kind: kind, Name: name, Action: action, Operator: operator, Timestamp: time.Now()})
	return nil
}

func (m *mockStore) CurrentRevision(_ context.Context, ns string) (int64, error) {
	return m.revision, nil
}
func (m *mockStore) WatchFrom(_ context.Context, ns string, sinceRevision int64) ([]store.ChangeEvent, int64, error) {
	var events []store.ChangeEvent
	for _, e := range m.changes {
		if e.Revision > sinceRevision {
			events = append(events, e)
		}
	}
	return events, m.revision, nil
}

func (m *mockStore) ListNamespaces(_ context.Context) ([]string, error) {
	return []string{"default"}, nil
}
func (m *mockStore) CreateNamespace(_ context.Context, name string) error { return nil }

func (m *mockStore) UpsertGatewayInstances(_ context.Context, ns string, instances []store.GatewayInstanceStatus) error {
	m.instances[ns] = instances
	return nil
}
func (m *mockStore) ListGatewayInstances(_ context.Context, ns string) ([]store.GatewayInstanceStatus, error) {
	return m.instances[ns], nil
}
func (m *mockStore) UpsertControllerStatus(_ context.Context, ns string, ctrl *store.ControllerStatus) error {
	m.ctrl[ns] = ctrl
	return nil
}
func (m *mockStore) GetControllerStatus(_ context.Context, ns string) (*store.ControllerStatus, error) {
	return m.ctrl[ns], nil
}
func (m *mockStore) MarkStaleInstances(_ context.Context, threshold time.Duration) ([]store.StaleEntry, error) {
	return nil, nil
}
func (m *mockStore) MarkStaleControllers(_ context.Context, threshold time.Duration) ([]store.StaleEntry, error) {
	return nil, nil
}

func (m *mockStore) ListGrafanaDashboards(_ context.Context, ns string) ([]store.GrafanaDashboard, error) {
	return m.dashboards[ns], nil
}
func (m *mockStore) PutGrafanaDashboard(_ context.Context, ns string, d *store.GrafanaDashboard) (*store.GrafanaDashboard, error) {
	if d.ID == 0 {
		d.ID = m.nextID
		m.nextID++
	}
	m.dashboards[ns] = append(m.dashboards[ns], *d)
	return d, nil
}
func (m *mockStore) DeleteGrafanaDashboard(_ context.Context, ns string, id int64) error {
	var filtered []store.GrafanaDashboard
	for _, d := range m.dashboards[ns] {
		if d.ID != id {
			filtered = append(filtered, d)
		}
	}
	m.dashboards[ns] = filtered
	return nil
}

func (m *mockStore) ListAPICredentials(_ context.Context, ns string) ([]store.APICredential, error) {
	return m.creds[ns], nil
}
func (m *mockStore) GetAPICredentialByAK(_ context.Context, accessKey string) (*store.APICredential, error) {
	return m.credsByAK[accessKey], nil
}
func (m *mockStore) CreateAPICredential(_ context.Context, ns string, cred *store.APICredential) (*store.APICredential, error) {
	cred.ID = m.nextID
	m.nextID++
	cred.Namespace = ns
	m.creds[ns] = append(m.creds[ns], *cred)
	m.credsByAK[cred.AccessKey] = cred
	return cred, nil
}
func (m *mockStore) UpdateAPICredential(_ context.Context, ns string, cred *store.APICredential) error {
	return nil
}
func (m *mockStore) DeleteAPICredential(_ context.Context, ns string, id int64) error {
	var filtered []store.APICredential
	for _, c := range m.creds[ns] {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	m.creds[ns] = filtered
	return nil
}

func (m *mockStore) UpsertUser(_ context.Context, user *store.User) error  { return nil }
func (m *mockStore) GetUser(_ context.Context, sub string) (*store.User, error) {
	return nil, nil
}
func (m *mockStore) ListUsers(_ context.Context) ([]store.User, error)     { return nil, nil }
func (m *mockStore) SetUserAdmin(_ context.Context, sub string, isAdmin bool) error {
	return nil
}
func (m *mockStore) GetUserPasswordHash(_ context.Context, sub string) (string, error) {
	return "", nil
}
func (m *mockStore) UpdateUserPassword(_ context.Context, sub, passwordHash string) error {
	return nil
}
func (m *mockStore) SetMustChangePassword(_ context.Context, sub string, must bool) error {
	return nil
}
func (m *mockStore) DeleteUser(_ context.Context, sub string) error {
	return nil
}
func (m *mockStore) GetActiveSigningKey(_ context.Context) (*store.JWTSigningKey, error) {
	return nil, nil
}
func (m *mockStore) GetSigningKeyByID(_ context.Context, kid string) (*store.JWTSigningKey, error) {
	return nil, nil
}
func (m *mockStore) ListValidSigningKeys(_ context.Context) ([]store.JWTSigningKey, error) {
	return nil, nil
}
func (m *mockStore) CreateSigningKey(_ context.Context, key *store.JWTSigningKey) error {
	return nil
}
func (m *mockStore) RotateSigningKey(_ context.Context, gracePeriod time.Duration) (*store.JWTSigningKey, error) {
	return &store.JWTSigningKey{KID: "mock-kid"}, nil
}

func (m *mockStore) ListNamespaceMembers(_ context.Context, ns string) ([]store.NamespaceMember, error) {
	return nil, nil
}
func (m *mockStore) GetNamespaceMember(_ context.Context, ns, userSub string) (*store.NamespaceMember, error) {
	return nil, nil
}
func (m *mockStore) SetNamespaceMember(_ context.Context, ns, userSub string, role store.NamespaceRole) error {
	return nil
}
func (m *mockStore) RemoveNamespaceMember(_ context.Context, ns, userSub string) error {
	return nil
}

func (m *mockStore) ListGroupBindings(_ context.Context, ns string) ([]store.GroupBinding, error) {
	return nil, nil
}
func (m *mockStore) SetGroupBinding(_ context.Context, ns, group string, role store.NamespaceRole) error {
	return nil
}
func (m *mockStore) RemoveGroupBinding(_ context.Context, ns, group string) error { return nil }
func (m *mockStore) GetEffectiveRoleByGroups(_ context.Context, ns string, groups []string) (*store.NamespaceRole, error) {
	return nil, nil
}

type notFoundError struct{ name string }

func (e *notFoundError) Error() string { return e.name + " not found" }

// ── Test Helpers ────────────────────────────────────

func testLogger() *zap.SugaredLogger {
	l, _ := zap.NewDevelopment()
	return l.Sugar()
}

// withNamespace injects the given namespace into the request context.
func withNamespace(r *http.Request, ns string) *http.Request {
	ctx := context.WithValue(r.Context(), namespaceKey, ns)
	return r.WithContext(ctx)
}

// setPathValue sets a Go 1.22+ path parameter value on the request.
func setPathValue(r *http.Request, key, value string) {
	r.SetPathValue(key, value)
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	return m
}

func jsonBody(v any) *bytes.Buffer {
	b, _ := json.Marshal(v)
	return bytes.NewBuffer(b)
}

// ── Domain Handler Tests ────────────────────────────

func TestDomainHandler_CreateDomain(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	body := jsonBody(model.DomainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []model.RouteConfig{
			{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}}, Status: 1},
		},
	})

	r := httptest.NewRequest("POST", "/api/v1/domains", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateDomain(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(1), resp["version"])
}

func TestDomainHandler_CreateDomain_Conflict(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	d := &model.DomainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []model.RouteConfig{
			{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}}, Status: 1},
		},
	}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	body := jsonBody(d)
	r := httptest.NewRequest("POST", "/api/v1/domains", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateDomain(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDomainHandler_CreateDomain_MissingName(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	body := jsonBody(model.DomainConfig{Hosts: []string{"a.com"}})
	r := httptest.NewRequest("POST", "/api/v1/domains", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateDomain(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDomainHandler_CreateDomain_InvalidJSON(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	r := httptest.NewRequest("POST", "/api/v1/domains", bytes.NewBufferString("{invalid"))
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateDomain(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDomainHandler_GetDomain(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	d := &model.DomainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []model.RouteConfig{
			{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}}},
		},
	}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	r := httptest.NewRequest("GET", "/api/v1/domains/api", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "api")
	w := httptest.NewRecorder()

	h.GetDomain(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, "api", resp["name"])
}

func TestDomainHandler_GetDomain_NotFound(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/domains/nonexistent", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "nonexistent")
	w := httptest.NewRecorder()

	h.GetDomain(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDomainHandler_ListDomains(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	d := &model.DomainConfig{Name: "api", Hosts: []string{"api.example.com"}}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	r := httptest.NewRequest("GET", "/api/v1/domains", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ListDomains(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(1), resp["total"])
}

func TestDomainHandler_UpdateDomain(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	d := &model.DomainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []model.RouteConfig{
			{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "c", Weight: 1}}},
		},
	}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	updated := model.DomainConfig{
		Hosts: []string{"api-v2.example.com"},
		Routes: []model.RouteConfig{
			{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "c", Weight: 1}}},
		},
	}
	body := jsonBody(updated)
	r := httptest.NewRequest("PUT", "/api/v1/domains/api", body)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "api")
	w := httptest.NewRecorder()

	h.UpdateDomain(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDomainHandler_UpdateDomain_NotFound(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	body := jsonBody(model.DomainConfig{Hosts: []string{"a.com"}})
	r := httptest.NewRequest("PUT", "/api/v1/domains/nonexistent", body)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "nonexistent")
	w := httptest.NewRecorder()

	h.UpdateDomain(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDomainHandler_DeleteDomain(t *testing.T) {
	ms := newMockStore()
	h := NewDomainHandler(ms, testLogger())

	d := &model.DomainConfig{Name: "api", Hosts: []string{"a.com"}}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	r := httptest.NewRequest("DELETE", "/api/v1/domains/api", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "api")
	w := httptest.NewRecorder()

	h.DeleteDomain(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── Cluster Handler Tests ───────────────────────────

func TestClusterHandler_CreateCluster(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	body := jsonBody(model.ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Timeout: model.TimeoutConfig{Connect: 3, Send: 6, Read: 6},
		Nodes:   []model.UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	})

	r := httptest.NewRequest("POST", "/api/v1/clusters", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateCluster(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestClusterHandler_CreateCluster_Conflict(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	c := &model.ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Timeout: model.TimeoutConfig{Connect: 1, Read: 1},
		Nodes:   []model.UpstreamNode{{Host: "h", Port: 80, Weight: 1}},
	}
	ms.PutCluster(context.Background(), "default", c, "create", "test")

	body := jsonBody(c)
	r := httptest.NewRequest("POST", "/api/v1/clusters", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateCluster(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestClusterHandler_CreateCluster_MissingName(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	body := jsonBody(model.ClusterConfig{LBType: "roundrobin"})
	r := httptest.NewRequest("POST", "/api/v1/clusters", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateCluster(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestClusterHandler_GetCluster(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	c := &model.ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Timeout: model.TimeoutConfig{Connect: 1, Read: 1},
		Nodes:   []model.UpstreamNode{{Host: "h", Port: 80, Weight: 1}},
	}
	ms.PutCluster(context.Background(), "default", c, "create", "test")

	r := httptest.NewRequest("GET", "/api/v1/clusters/backend", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "backend")
	w := httptest.NewRecorder()

	h.GetCluster(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestClusterHandler_GetCluster_NotFound(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/clusters/nonexistent", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "nonexistent")
	w := httptest.NewRecorder()

	h.GetCluster(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestClusterHandler_DeleteCluster(t *testing.T) {
	ms := newMockStore()
	h := NewClusterHandler(ms, testLogger())

	c := &model.ClusterConfig{Name: "backend", LBType: "roundrobin", Timeout: model.TimeoutConfig{Connect: 1, Read: 1}, Nodes: []model.UpstreamNode{{Host: "h", Port: 80, Weight: 1}}}
	ms.PutCluster(context.Background(), "default", c, "create", "test")

	r := httptest.NewRequest("DELETE", "/api/v1/clusters/backend", nil)
	r = withNamespace(r, "default")
	setPathValue(r, "name", "backend")
	w := httptest.NewRecorder()

	h.DeleteCluster(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── Watch Handler Tests ─────────────────────────────

func TestWatchHandler_GetRevision(t *testing.T) {
	ms := newMockStore()
	h := NewWatchHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/config/revision", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.GetRevision(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(0), resp["revision"])
}

func TestWatchHandler_WatchConfig(t *testing.T) {
	ms := newMockStore()
	h := NewWatchHandler(ms, testLogger())

	// Create some data to generate change events
	d := &model.DomainConfig{Name: "api", Hosts: []string{"a.com"}}
	ms.PutDomain(context.Background(), "default", d, "create", "test")

	r := httptest.NewRequest("GET", "/api/v1/config/watch?revision=0", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.WatchConfig(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(1), resp["total"])
}

func TestWatchHandler_WatchConfig_InvalidRevision(t *testing.T) {
	ms := newMockStore()
	h := NewWatchHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/config/watch?revision=abc", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.WatchConfig(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Config Handler Tests ────────────────────────────

func TestRouteHandler_GetConfig(t *testing.T) {
	ms := newMockStore()
	h := NewRouteHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/config", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.GetConfig(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRouteHandler_ValidateConfig_Valid(t *testing.T) {
	ms := newMockStore()
	h := NewRouteHandler(ms, testLogger())

	cfg := model.GatewayConfig{
		Domains: []model.DomainConfig{
			{Name: "api", Hosts: []string{"a.com"}, Routes: []model.RouteConfig{
				{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}}},
			}},
		},
		Clusters: []model.ClusterConfig{
			{Name: "backend", LBType: "roundrobin", Timeout: model.TimeoutConfig{Connect: 1, Read: 1}, Nodes: []model.UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
		},
	}

	body := jsonBody(cfg)
	r := httptest.NewRequest("POST", "/api/v1/config/validate", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ValidateConfig(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, true, resp["valid"])
}

func TestRouteHandler_ValidateConfig_Invalid(t *testing.T) {
	ms := newMockStore()
	h := NewRouteHandler(ms, testLogger())

	cfg := model.GatewayConfig{
		Domains: []model.DomainConfig{
			{Name: "", Hosts: []string{}},
		},
	}

	body := jsonBody(cfg)
	r := httptest.NewRequest("POST", "/api/v1/config/validate", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ValidateConfig(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, false, resp["valid"])
}

func TestRouteHandler_PutConfig(t *testing.T) {
	ms := newMockStore()
	h := NewRouteHandler(ms, testLogger())

	cfg := model.GatewayConfig{
		Domains: []model.DomainConfig{
			{Name: "api", Hosts: []string{"a.com"}, Routes: []model.RouteConfig{
				{Name: "r1", URI: "/", Clusters: []model.WeightedCluster{{Name: "backend", Weight: 100}}},
			}},
		},
		Clusters: []model.ClusterConfig{
			{Name: "backend", LBType: "roundrobin", Timeout: model.TimeoutConfig{Connect: 1, Read: 1}, Nodes: []model.UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
		},
	}

	body := jsonBody(cfg)
	r := httptest.NewRequest("PUT", "/api/v1/config", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.PutConfig(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── Audit Handler Tests ─────────────────────────────

func TestAuditHandler_ListAuditLog(t *testing.T) {
	ms := newMockStore()
	h := NewAuditHandler(ms, testLogger())

	ms.InsertAuditLog(context.Background(), "default", "domain", "api", "create", "test")

	r := httptest.NewRequest("GET", "/api/v1/audit", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ListAuditLog(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(1), resp["total"])
}

func TestAuditHandler_ListAuditLog_DefaultLimit(t *testing.T) {
	ms := newMockStore()
	h := NewAuditHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/audit", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ListAuditLog(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, float64(50), resp["limit"])
}

// ── Status Handler Tests ────────────────────────────

func TestStatusHandler_ReportAndGetController(t *testing.T) {
	ms := newMockStore()
	h := NewStatusHandler(ms, testLogger())

	ctrl := store.ControllerStatus{
		ID:              "ctrl-1",
		Status:          "running",
		StartedAt:       time.Now().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().Format(time.RFC3339),
		ConfigRevision:  42,
	}
	body := jsonBody(ctrl)
	r := httptest.NewRequest("PUT", "/api/v1/status/controller", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ReportController(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	// Now get it
	r2 := httptest.NewRequest("GET", "/api/v1/status/controller", nil)
	r2 = withNamespace(r2, "default")
	w2 := httptest.NewRecorder()

	h.GetController(w2, r2)
	assert.Equal(t, http.StatusOK, w2.Code)

	resp := decodeResp(t, w2)
	ctrlMap := resp["controller"].(map[string]any)
	assert.Equal(t, "ctrl-1", ctrlMap["id"])
}

func TestStatusHandler_ReportInstances(t *testing.T) {
	ms := newMockStore()
	h := NewStatusHandler(ms, testLogger())

	body := jsonBody(map[string]any{
		"instances": []store.GatewayInstanceStatus{
			{ID: "gw-1", Status: "running"},
			{ID: "gw-2", Status: "running"},
		},
	})
	r := httptest.NewRequest("PUT", "/api/v1/status/instances", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.ReportInstances(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	// List them
	r2 := httptest.NewRequest("GET", "/api/v1/status/instances", nil)
	r2 = withNamespace(r2, "default")
	w2 := httptest.NewRecorder()

	h.ListInstances(w2, r2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestStatusHandler_AggregateStatus(t *testing.T) {
	ms := newMockStore()
	h := NewStatusHandler(ms, testLogger())

	r := httptest.NewRequest("GET", "/api/v1/status", nil)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.AggregateStatus(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── Credential Handler Tests ────────────────────────

func TestCredentialHandler_CreateAndList(t *testing.T) {
	ms := newMockStore()
	h := NewCredentialHandler(ms, testLogger())

	body := jsonBody(map[string]any{
		"description": "test credential",
		"scopes":      []string{"config:read", "config:write"},
	})
	r := httptest.NewRequest("POST", "/api/v1/credentials", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateCredential(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)

	resp := decodeResp(t, w)
	assert.NotEmpty(t, resp["access_key"])
	assert.NotEmpty(t, resp["secret_key"])

	// List
	r2 := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	r2 = withNamespace(r2, "default")
	w2 := httptest.NewRecorder()

	h.ListCredentials(w2, r2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestCredentialHandler_CreateWithInvalidScope(t *testing.T) {
	ms := newMockStore()
	h := NewCredentialHandler(ms, testLogger())

	body := jsonBody(map[string]any{
		"description": "bad",
		"scopes":      []string{"invalid:scope"},
	})
	r := httptest.NewRequest("POST", "/api/v1/credentials", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.CreateCredential(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Grafana Handler Tests ───────────────────────────

func TestGrafanaHandler_CreateAndDelete(t *testing.T) {
	ms := newMockStore()
	h := NewGrafanaHandler(ms, testLogger())

	body := jsonBody(store.GrafanaDashboard{
		Name: "Overview",
		URL:  "https://grafana.example.com/d/abc",
	})
	r := httptest.NewRequest("POST", "/api/v1/grafana/dashboards", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.PutDashboard(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)

	resp := decodeResp(t, w)
	assert.Equal(t, "Overview", resp["name"])

	// Delete
	r2 := httptest.NewRequest("DELETE", "/api/v1/grafana/dashboards/1", nil)
	r2 = withNamespace(r2, "default")
	setPathValue(r2, "id", "1")
	w2 := httptest.NewRecorder()

	h.DeleteDashboard(w2, r2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestGrafanaHandler_PutDashboard_MissingFields(t *testing.T) {
	ms := newMockStore()
	h := NewGrafanaHandler(ms, testLogger())

	body := jsonBody(store.GrafanaDashboard{Name: ""})
	r := httptest.NewRequest("POST", "/api/v1/grafana/dashboards", body)
	r = withNamespace(r, "default")
	w := httptest.NewRecorder()

	h.PutDashboard(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Middleware Tests ─────────────────────────────────

func TestNamespaceMiddleware_Default(t *testing.T) {
	var capturedNS string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNS = NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	NamespaceMiddleware(next).ServeHTTP(w, r)
	assert.Equal(t, "default", capturedNS)
}

func TestNamespaceMiddleware_FromHeader(t *testing.T) {
	var capturedNS string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNS = NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Hermes-Namespace", "production")
	w := httptest.NewRecorder()

	NamespaceMiddleware(next).ServeHTTP(w, r)
	assert.Equal(t, "production", capturedNS)
}

func TestNamespaceMiddleware_FromQuery(t *testing.T) {
	var capturedNS string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNS = NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/?namespace=staging", nil)
	w := httptest.NewRecorder()

	NamespaceMiddleware(next).ServeHTTP(w, r)
	assert.Equal(t, "staging", capturedNS)
}

func TestRequireScope_Allowed(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	identity := &Identity{Subject: "test", Scopes: []string{"config:read"}}
	r := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(r.Context(), identityKey, identity)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	RequireScope("config:read")(next).ServeHTTP(w, r)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequireScope_Denied(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	identity := &Identity{Subject: "test", Scopes: []string{"config:read"}}
	r := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(r.Context(), identityKey, identity)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	RequireScope("config:write")(next).ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRequireScope_NoIdentity_Bootstrap(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	RequireScope("config:read")(next).ServeHTTP(w, r)
	assert.True(t, called)
}

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("OPTIONS", "/", nil)
	w := httptest.NewRecorder()

	CORS(next).ServeHTTP(w, r)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestRecovery_NoPanic(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	Recovery(testLogger(), next).ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRecovery_WithPanic(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	Recovery(testLogger(), next).ServeHTTP(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── Identity Tests ──────────────────────────────────

func TestIdentity_HasScope(t *testing.T) {
	id := &Identity{Scopes: []string{"config:read", "config:write"}}
	assert.True(t, id.HasScope("config:read"))
	assert.True(t, id.HasScope("config:write"))
	assert.False(t, id.HasScope("admin:users"))
}

func TestIdentityFromContext_Nil(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, IdentityFromContext(ctx))
}

// ── Operator Tests ──────────────────────────────────

func TestOperator_NoAuth(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	assert.Empty(t, Operator(r))
}

// ── parseHMACAuthHeader Tests ───────────────────────

func TestParseHMACAuthHeader_Valid(t *testing.T) {
	ak, sig, err := parseHMACAuthHeader("HMAC-SHA256 Credential=ak123,Signature=sig456")
	require.NoError(t, err)
	assert.Equal(t, "ak123", ak)
	assert.Equal(t, "sig456", sig)
}

func TestParseHMACAuthHeader_Invalid(t *testing.T) {
	_, _, err := parseHMACAuthHeader("Bearer token123")
	assert.Error(t, err)
}

func TestParseHMACAuthHeader_MissingFields(t *testing.T) {
	_, _, err := parseHMACAuthHeader("HMAC-SHA256 Credential=ak123")
	assert.Error(t, err)
}
