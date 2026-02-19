package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── ValidateDomain Tests ──────────────────────────

func TestValidateDomain_Valid(t *testing.T) {
	d := &DomainConfig{
		Name:  "api",
		Hosts: []string{"api.example.com"},
		Routes: []RouteConfig{
			{
				Name:     "catch-all",
				URI:      "/*",
				Clusters: []WeightedCluster{{Name: "backend", Weight: 100}},
				Status:   1,
			},
		},
	}
	errs := ValidateDomain(d, map[string]bool{"backend": true})
	assert.Empty(t, errs)
}

func TestValidateDomain_MissingName(t *testing.T) {
	d := &DomainConfig{
		Hosts:  []string{"api.example.com"},
		Routes: []RouteConfig{{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}}},
	}
	errs := ValidateDomain(d, nil)
	require.NotEmpty(t, errs)
	assert.Equal(t, "domains[0].name", errs[0].Field)
}

func TestValidateDomain_MissingHosts(t *testing.T) {
	d := &DomainConfig{
		Name:   "api",
		Routes: []RouteConfig{{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}}},
	}
	errs := ValidateDomain(d, nil)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "host")
}

func TestValidateDomain_EmptyHost(t *testing.T) {
	d := &DomainConfig{
		Name:   "api",
		Hosts:  []string{""},
		Routes: []RouteConfig{{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}}},
	}
	errs := ValidateDomain(d, nil)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "empty host")
}

// ── ValidateRoutes Tests ──────────────────────────

func TestValidateRoutes_MissingRouteName(t *testing.T) {
	routes := []RouteConfig{
		{URI: "/api", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Equal(t, "routes[0].name", errs[0].Field)
}

func TestValidateRoutes_DuplicateRouteName(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", URI: "/a", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}},
		{Name: "r1", URI: "/b", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "duplicate")
}

func TestValidateRoutes_MissingURI(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Equal(t, "routes[0].uri", errs[0].Field)
}

func TestValidateRoutes_URIMustStartWithSlash(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", URI: "api", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "start with /")
}

func TestValidateRoutes_MissingClusterRef(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", URI: "/api"},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "cluster")
}

func TestValidateRoutes_ClusterNotFound(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", URI: "/api", Clusters: []WeightedCluster{{Name: "nonexistent", Weight: 1}}},
	}
	errs := ValidateRoutes(routes, map[string]bool{"backend": true}, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "not found")
}

func TestValidateRoutes_NegativeWeight(t *testing.T) {
	routes := []RouteConfig{
		{Name: "r1", URI: "/api", Clusters: []WeightedCluster{{Name: "c", Weight: -1}}},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, ">= 0")
}

func TestValidateRoutes_InvalidHeaderMatchType(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:     "r1",
			URI:      "/api",
			Clusters: []WeightedCluster{{Name: "c", Weight: 1}},
			Headers:  []HeaderMatcher{{Name: "X-Test", MatchType: "invalid"}},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "match_type")
}

func TestValidateRoutes_ValidHeaderMatchTypes(t *testing.T) {
	for _, mt := range []string{"", "exact", "prefix", "regex", "present"} {
		routes := []RouteConfig{
			{
				Name:     "r1",
				URI:      "/api",
				Clusters: []WeightedCluster{{Name: "c", Weight: 1}},
				Headers:  []HeaderMatcher{{Name: "X-Test", MatchType: mt}},
			},
		}
		errs := ValidateRoutes(routes, nil, "routes")
		assert.Empty(t, errs, "match_type %q should be valid", mt)
	}
}

func TestValidateRoutes_HeaderMissingName(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:     "r1",
			URI:      "/api",
			Clusters: []WeightedCluster{{Name: "c", Weight: 1}},
			Headers:  []HeaderMatcher{{MatchType: "exact"}},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "headers")
}

func TestValidateRoutes_RateLimitReqMode(t *testing.T) {
	rate := 10.0
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	assert.Empty(t, errs)
}

func TestValidateRoutes_RateLimitReqModeMissingRate(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req"},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "rate")
}

func TestValidateRoutes_RateLimitCountMode(t *testing.T) {
	count := 100
	tw := 60
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "count", Count: &count, TimeWindow: &tw},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	assert.Empty(t, errs)
}

func TestValidateRoutes_RateLimitInvalidMode(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "unknown"},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "'req' or 'count'")
}

func TestValidateRoutes_InvalidStatus(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:     "r1",
			URI:      "/api",
			Clusters: []WeightedCluster{{Name: "c", Weight: 1}},
			Status:   2,
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "0 or 1")
}

// ── ValidateCluster Tests ─────────────────────────

func TestValidateCluster_Valid(t *testing.T) {
	c := &ClusterConfig{
		Name:   "backend",
		LBType: "roundrobin",
		Timeout: TimeoutConfig{
			Connect: 3.0,
			Read:    6.0,
		},
		Nodes: []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	assert.Empty(t, errs)
}

func TestValidateCluster_MissingName(t *testing.T) {
	c := &ClusterConfig{
		LBType:  "roundrobin",
		Timeout: TimeoutConfig{Connect: 1, Read: 1},
		Nodes:   []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Equal(t, "clusters[0].name", errs[0].Field)
}

func TestValidateCluster_MissingLBType(t *testing.T) {
	c := &ClusterConfig{
		Name:    "backend",
		Timeout: TimeoutConfig{Connect: 1, Read: 1},
		Nodes:   []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "type")
}

func TestValidateCluster_NoNodesOrDiscovery(t *testing.T) {
	c := &ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Timeout: TimeoutConfig{Connect: 1, Read: 1},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "nodes or discovery")
}

func TestValidateCluster_WithDiscovery(t *testing.T) {
	dt := "consul"
	sn := "my-service"
	c := &ClusterConfig{
		Name:          "backend",
		LBType:        "roundrobin",
		Timeout:       TimeoutConfig{Connect: 1, Read: 1},
		DiscoveryType: &dt,
		ServiceName:   &sn,
	}
	errs := ValidateCluster(c)
	assert.Empty(t, errs)
}

func TestValidateCluster_InvalidTimeout(t *testing.T) {
	c := &ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Timeout: TimeoutConfig{Connect: 0, Read: 0},
		Nodes:   []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "> 0")
}

// ── ValidateClusters Tests ────────────────────────

func TestValidateClusters_DuplicateName(t *testing.T) {
	clusters := []ClusterConfig{
		{Name: "c1", LBType: "roundrobin", Timeout: TimeoutConfig{Connect: 1, Read: 1}, Nodes: []UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
		{Name: "c1", LBType: "roundrobin", Timeout: TimeoutConfig{Connect: 1, Read: 1}, Nodes: []UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
	}
	errs := ValidateClusters(clusters)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "duplicate")
}

// ── ValidateConfig Tests ──────────────────────────

func TestValidateConfig_CrossReference(t *testing.T) {
	cfg := &GatewayConfig{
		Domains: []DomainConfig{
			{
				Name:  "api",
				Hosts: []string{"api.example.com"},
				Routes: []RouteConfig{
					{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "backend", Weight: 100}}},
				},
			},
		},
		Clusters: []ClusterConfig{
			{Name: "backend", LBType: "roundrobin", Timeout: TimeoutConfig{Connect: 1, Read: 1}, Nodes: []UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
		},
	}
	errs := ValidateConfig(cfg)
	assert.Empty(t, errs)
}

func TestValidateConfig_MissingClusterReference(t *testing.T) {
	cfg := &GatewayConfig{
		Domains: []DomainConfig{
			{
				Name:  "api",
				Hosts: []string{"api.example.com"},
				Routes: []RouteConfig{
					{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "nonexistent", Weight: 100}}},
				},
			},
		},
		Clusters: []ClusterConfig{
			{Name: "backend", LBType: "roundrobin", Timeout: TimeoutConfig{Connect: 1, Read: 1}, Nodes: []UpstreamNode{{Host: "h", Port: 80, Weight: 1}}},
		},
	}
	errs := ValidateConfig(cfg)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "not found")
}

func TestValidateConfig_Empty(t *testing.T) {
	cfg := &GatewayConfig{}
	errs := ValidateConfig(cfg)
	assert.Empty(t, errs)
}

// ── ValidateDomains Tests ─────────────────────────

func TestValidateDomains_DuplicateName(t *testing.T) {
	domains := []DomainConfig{
		{Name: "api", Hosts: []string{"a.com"}, Routes: []RouteConfig{{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}}}},
		{Name: "api", Hosts: []string{"b.com"}, Routes: []RouteConfig{{Name: "r1", URI: "/", Clusters: []WeightedCluster{{Name: "c", Weight: 1}}}}},
	}
	errs := ValidateDomains(domains, nil)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "duplicate")
}

// ── New Rate Limit Validation Tests ───────────────

func TestValidateRoutes_RateLimitBurstNegative(t *testing.T) {
	rate := 10.0
	burst := -1
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate, Burst: &burst},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "burst")
}

func TestValidateRoutes_RateLimitCountModeMissingTimeWindow(t *testing.T) {
	count := 100
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "count", Count: &count},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "time_window")
}

func TestValidateRoutes_RateLimitInvalidKey(t *testing.T) {
	rate := 10.0
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate, Key: "invalid_key"},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "key")
}

func TestValidateRoutes_RateLimitValidKeys(t *testing.T) {
	rate := 10.0
	for _, key := range []string{"", "route", "host_uri", "remote_addr", "uri"} {
		routes := []RouteConfig{
			{
				Name:      "r1",
				URI:       "/api",
				Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
				RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate, Key: key},
			},
		}
		errs := ValidateRoutes(routes, nil, "routes")
		assert.Empty(t, errs, "key %q should be valid", key)
	}
}

func TestValidateRoutes_RateLimitInvalidRejectedCode(t *testing.T) {
	rate := 10.0
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate, RejectedCode: 200},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "rejected_code")
}

func TestValidateRoutes_RateLimitRejectedCodeZeroIsOK(t *testing.T) {
	rate := 10.0
	routes := []RouteConfig{
		{
			Name:      "r1",
			URI:       "/api",
			Clusters:  []WeightedCluster{{Name: "c", Weight: 1}},
			RateLimit: &RateLimitConfig{Mode: "req", Rate: &rate, RejectedCode: 0},
		},
	}
	errs := ValidateRoutes(routes, nil, "routes")
	assert.Empty(t, errs)
}

// ── New Cluster Validation Tests ──────────────────

func TestValidateCluster_InvalidScheme(t *testing.T) {
	c := &ClusterConfig{
		Name:    "backend",
		LBType:  "roundrobin",
		Scheme:  "ftp",
		Timeout: TimeoutConfig{Connect: 1, Read: 1},
		Nodes:   []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "scheme")
}

func TestValidateCluster_ValidSchemes(t *testing.T) {
	for _, scheme := range []string{"http", "https", ""} {
		c := &ClusterConfig{
			Name:    "backend",
			LBType:  "roundrobin",
			Scheme:  scheme,
			Timeout: TimeoutConfig{Connect: 1, Read: 1},
			Nodes:   []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
		}
		errs := ValidateCluster(c)
		assert.Empty(t, errs, "scheme %q should be valid", scheme)
	}
}

func TestValidateCluster_InvalidPassHost(t *testing.T) {
	c := &ClusterConfig{
		Name:     "backend",
		LBType:   "roundrobin",
		PassHost: "invalid",
		Timeout:  TimeoutConfig{Connect: 1, Read: 1},
		Nodes:    []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "pass_host")
}

func TestValidateCluster_RewriteRequiresUpstreamHost(t *testing.T) {
	c := &ClusterConfig{
		Name:     "backend",
		LBType:   "roundrobin",
		PassHost: "rewrite",
		Timeout:  TimeoutConfig{Connect: 1, Read: 1},
		Nodes:    []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Field, "upstream_host")
}

func TestValidateCluster_RewriteWithUpstreamHost(t *testing.T) {
	host := "upstream.example.com"
	c := &ClusterConfig{
		Name:         "backend",
		LBType:       "roundrobin",
		PassHost:     "rewrite",
		UpstreamHost: &host,
		Timeout:      TimeoutConfig{Connect: 1, Read: 1},
		Nodes:        []UpstreamNode{{Host: "10.0.0.1", Port: 8080, Weight: 100}},
	}
	errs := ValidateCluster(c)
	assert.Empty(t, errs)
}
