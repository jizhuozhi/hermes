package model

// GatewayConfig mirrors the Rust data-plane GatewayConfig exactly.
// Following the nginx model: every route belongs to a domain.
// Global/fallback routes live under the special "_default" domain with hosts: ["_"].
type GatewayConfig struct {
	Consul           ConsulConfig           `json:"consul"`
	Etcd             EtcdConfig             `json:"etcd"`
	Domains          []DomainConfig         `json:"domains"`
	Clusters         []ClusterConfig        `json:"clusters"`
	Registration     RegistrationConfig     `json:"registration"`
	InstanceRegistry InstanceRegistryConfig `json:"instance_registry"`
}

type ConsulConfig struct {
	Address          string  `json:"address"`
	Datacenter       *string `json:"datacenter,omitempty"`
	Token            *string `json:"token,omitempty"`
	PollIntervalSecs int     `json:"poll_interval_secs"`
}

type EtcdConfig struct {
	Endpoints     []string `json:"endpoints"`
	DomainPrefix  string   `json:"domain_prefix,omitempty"`
	ClusterPrefix string   `json:"cluster_prefix,omitempty"`
	Username      *string  `json:"username,omitempty"`
	Password      *string  `json:"password,omitempty"`
}

// DomainConfig is the top-level business domain boundary.
// Each domain can match multiple hosts and owns its route table.
// Access control (OIDC + GBAC + RBAC) is enforced at the controlplane
// layer — the data-plane / etcd are auth-unaware.
type DomainConfig struct {
	Name   string        `json:"name"`
	Hosts  []string      `json:"hosts"`
	Routes []RouteConfig `json:"routes"`
}

// RouteConfig references one or more clusters by name with weights.
// Routes no longer embed upstream configuration directly.
type RouteConfig struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	URI                      string            `json:"uri"`
	Methods                  []string          `json:"methods"`
	Headers                  []HeaderMatcher   `json:"headers,omitempty"`
	Priority                 int               `json:"priority"`
	Clusters                 []WeightedCluster `json:"clusters"`
	RateLimit                *RateLimitConfig  `json:"rate_limit,omitempty"`
	ClusterOverrideHeader    *string           `json:"cluster_override_header,omitempty"`
	RequestHeaderTransforms  []HeaderTransform `json:"request_header_transforms,omitempty"`
	ResponseHeaderTransforms []HeaderTransform `json:"response_header_transforms,omitempty"`
	MaxBodyBytes             *int64            `json:"max_body_bytes,omitempty"`
	EnableCompression        bool              `json:"enable_compression"`
	Status                   int               `json:"status"`
	Plugins                  interface{}       `json:"plugins,omitempty"`
}

// HeaderMatcher defines a header matching condition for a route.
// Multiple matchers on a route use AND semantics.
type HeaderMatcher struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	MatchType string `json:"match_type"` // "exact", "prefix", "regex", "present"
	Invert    bool   `json:"invert"`
}

// HeaderTransform defines a single header modification rule.
// Action: "set" (default) replaces the header, "add" appends, "remove" deletes.
// Used for request-phase traffic coloring and response-phase header injection.
type HeaderTransform struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Action string `json:"action"` // "set", "add", "remove"
}

// WeightedCluster is a reference to a cluster with a relative weight.
type WeightedCluster struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}

// ClusterConfig is an independent upstream definition.
// Multiple routes can reference the same cluster.
type ClusterConfig struct {
	Name           string                `json:"name"`
	LBType         string                `json:"type"`
	Timeout        TimeoutConfig         `json:"timeout"`
	Scheme         string                `json:"scheme"`
	PassHost       string                `json:"pass_host"`
	UpstreamHost   *string               `json:"upstream_host,omitempty"`
	Nodes          []UpstreamNode        `json:"nodes"`
	DiscoveryType  *string               `json:"discovery_type,omitempty"`
	ServiceName    *string               `json:"service_name,omitempty"`
	DiscoveryArgs  *DiscoveryArgs        `json:"discovery_args,omitempty"`
	KeepalivePool  KeepalivePoolConfig   `json:"keepalive_pool"`
	HealthCheck    *HealthCheckConfig    `json:"health_check,omitempty"`
	Retry          *RetryConfig          `json:"retry,omitempty"`
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty"`
	// TLSVerify controls whether the gateway verifies upstream TLS certificates.
	// Default false — typical for gateway scenarios where upstreams are internal
	// services using self-signed or private CA certificates.
	TLSVerify bool `json:"tls_verify"`
}

type TimeoutConfig struct {
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Read    float64 `json:"read"`
}

type UpstreamNode struct {
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	Weight   int               `json:"weight"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DiscoveryArgs struct {
	MetadataMatch map[string][]string `json:"metadata_match"`
}

type KeepalivePoolConfig struct {
	IdleTimeout int `json:"idle_timeout"`
	Requests    int `json:"requests"`
	Size        int `json:"size"`
}

type RateLimitConfig struct {
	Mode         string   `json:"mode"`
	Rate         *float64 `json:"rate,omitempty"`
	Burst        *int     `json:"burst,omitempty"`
	Count        *int     `json:"count,omitempty"`
	TimeWindow   *int     `json:"time_window,omitempty"`
	Key          string   `json:"key"`
	RejectedCode int      `json:"rejected_code"`
}

type HealthCheckConfig struct {
	Active *ActiveHealthCheck `json:"active,omitempty"`
}

type ActiveHealthCheck struct {
	Interval           int    `json:"interval"`
	Path               string `json:"path"`
	Port               *int   `json:"port,omitempty"`
	HealthyStatuses    []int  `json:"healthy_statuses"`
	HealthyThreshold   int    `json:"healthy_threshold"`
	UnhealthyThreshold int    `json:"unhealthy_threshold"`
	Timeout            int    `json:"timeout"`
	Concurrency        int    `json:"concurrency,omitempty"`
}

type RegistrationConfig struct {
	Enabled             bool              `json:"enabled"`
	ServiceName         string            `json:"service_name"`
	TTLSecs             int               `json:"ttl_secs"`
	DeregisterAfterSecs int               `json:"deregister_after_secs"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

type RetryConfig struct {
	Count                 int   `json:"count"`
	RetryOnStatuses       []int `json:"retry_on_statuses"`
	RetryOnConnectFailure bool  `json:"retry_on_connect_failure"`
	RetryOnTimeout        bool  `json:"retry_on_timeout"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int `json:"failure_threshold"`
	SuccessThreshold int `json:"success_threshold"`
	OpenDurationSecs int `json:"open_duration_secs"`
}

type InstanceRegistryConfig struct {
	Enabled      bool   `json:"enabled"`
	Prefix       string `json:"prefix"`
	LeaseTTLSecs int    `json:"lease_ttl_secs"`
}
