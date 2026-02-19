use serde::{Deserialize, Deserializer, Serialize};
use std::collections::HashMap;

/// Deserialize a `T` that implements `Default` — treats JSON `null` the same as
/// a missing field (returns `T::default()`).  Use with:
///   `#[serde(default, deserialize_with = "deserialize_null_default")]`
fn deserialize_null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

/// Top-level gateway configuration.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct GatewayConfig {
    /// Consul discovery settings.
    #[serde(default)]
    pub consul: ConsulConfig,

    /// etcd settings for dynamic config.
    #[serde(default)]
    pub etcd: EtcdConfig,

    /// Self-registration to Consul so upstream gateways can discover us.
    #[serde(default)]
    pub registration: RegistrationConfig,

    /// Instance registry for distributed rate limiting.
    /// Gateways register themselves in etcd and track peer count to split
    /// rate limits evenly across instances.
    #[serde(default)]
    pub instance_registry: InstanceRegistryConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConsulConfig {
    /// Consul HTTP API address.
    #[serde(default = "default_consul_addr")]
    pub address: String,

    /// Consul datacenter.
    #[serde(default)]
    pub datacenter: Option<String>,

    /// Consul ACL token.
    #[serde(default)]
    pub token: Option<String>,

    /// How often to poll consul for service changes (seconds).
    #[serde(default = "default_poll_interval")]
    pub poll_interval_secs: u64,
}

impl Default for ConsulConfig {
    fn default() -> Self {
        Self {
            address: default_consul_addr(),
            datacenter: None,
            token: None,
            poll_interval_secs: default_poll_interval(),
        }
    }
}

fn default_consul_addr() -> String {
    "http://127.0.0.1:8500".to_string()
}

fn default_poll_interval() -> u64 {
    10
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EtcdConfig {
    /// etcd endpoints.
    #[serde(default = "default_etcd_endpoints")]
    pub endpoints: Vec<String>,

    /// etcd key prefix for domain configuration.
    #[serde(default = "default_etcd_domain_prefix")]
    pub domain_prefix: String,

    /// etcd key prefix for cluster configuration.
    #[serde(default = "default_etcd_cluster_prefix")]
    pub cluster_prefix: String,

    /// etcd key prefix for controller metadata (e.g. config_revision).
    #[serde(default)]
    pub meta_prefix: Option<String>,

    /// Username for etcd auth.
    #[serde(default)]
    pub username: Option<String>,

    /// Password for etcd auth.
    #[serde(default)]
    pub password: Option<String>,
}

impl Default for EtcdConfig {
    fn default() -> Self {
        Self {
            endpoints: default_etcd_endpoints(),
            domain_prefix: default_etcd_domain_prefix(),
            cluster_prefix: default_etcd_cluster_prefix(),
            meta_prefix: None,
            username: None,
            password: None,
        }
    }
}

fn default_etcd_endpoints() -> Vec<String> {
    vec!["http://127.0.0.1:2379".to_string()]
}

fn default_etcd_domain_prefix() -> String {
    "/hermes/domains".to_string()
}

fn default_etcd_cluster_prefix() -> String {
    "/hermes/clusters".to_string()
}

/// A domain definition — the top-level business domain boundary.
///
/// Each domain can match multiple hosts (exact or wildcard) and contains
/// its own route table. Analogous to Nginx `server` blocks grouped by
/// business concern, or Envoy `VirtualHost` extended with multi-host support.
///
/// Domain-level concerns (TLS, CORS, auth policies, rate limiting quotas)
/// can be attached here. Access control (OIDC + GBAC + RBAC) is enforced
/// at the controlplane layer — the data-plane / etcd are auth-unaware.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DomainConfig {
    /// Unique domain name (e.g. "user-service", "payment", "admin").
    pub name: String,

    /// Host patterns this domain matches. Supports:
    /// - exact: `api.example.com`
    /// - wildcard suffix: `*.example.com`
    /// - wildcard prefix: `api.*`
    ///
    /// A domain with multiple hosts allows grouping related endpoints
    /// (e.g. `api.example.com` + `api-internal.example.com`) under one
    /// business domain for unified route management.
    pub hosts: Vec<String>,

    /// Routes belonging to this domain.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub routes: Vec<RouteConfig>,
}

/// A single route definition.
///
/// Routes no longer embed upstream configuration directly. Instead they
/// reference one or more `ClusterConfig` entries by name with weights,
/// enabling canary / blue-green / traffic-split at the routing layer.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RouteConfig {
    /// Unique route ID.
    #[serde(default)]
    pub id: String,

    /// Route name.
    #[serde(default)]
    pub name: String,

    /// URI pattern. Supports exact match, prefix match (e.g. `/v1/api/*`), and `/*` for catch-all.
    pub uri: String,

    /// Allowed HTTP methods. Empty means all methods.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub methods: Vec<String>,

    /// Header matchers. All conditions must match (AND semantics).
    /// Enables fine-grained routing based on request headers
    /// (e.g. API version negotiation, tenant routing, canary flags).
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub headers: Vec<HeaderMatcher>,

    /// Priority for route matching. Higher value = higher priority.
    /// More specific URIs automatically get higher priority.
    #[serde(default)]
    pub priority: i32,

    /// Weighted cluster references. Traffic is distributed across clusters
    /// according to their weights. This replaces the old inline `upstream` field.
    ///
    /// Example: `[{name:"prod", weight:95}, {name:"canary", weight:5}]`
    pub clusters: Vec<WeightedCluster>,

    /// Rate limiting configuration.
    #[serde(default)]
    pub rate_limit: Option<RateLimitConfig>,

    /// Optional header name for cluster override.
    /// When set, if the request carries this header, its value is used as the
    /// cluster name — bypassing weighted selection. Useful for per-environment
    /// testing (e.g. `X-Cluster-Override: canary`).
    #[serde(default)]
    pub cluster_override_header: Option<String>,

    /// Request-phase header transforms.
    /// Applied to upstream requests before forwarding. Useful for traffic coloring
    /// (e.g. injecting `X-Env: canary` so upstream services can branch behavior).
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub request_header_transforms: Vec<HeaderTransform>,

    /// Response-phase header transforms.
    /// Applied to downstream responses before sending to clients.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub response_header_transforms: Vec<HeaderTransform>,

    /// Maximum allowed request body size in bytes.
    /// When set, requests with `Content-Length` exceeding this limit are
    /// rejected with 413 (Payload Too Large) before reading the body.
    /// Streaming requests without `Content-Length` are checked during body
    /// buffering. `None` means no limit.
    #[serde(default)]
    pub max_body_bytes: Option<u64>,

    /// Enable response compression (gzip / brotli) for this route.
    /// When true, responses are compressed using streaming compression
    /// if the client sends a supported `Accept-Encoding` header and the
    /// upstream hasn't already set `Content-Encoding`.
    #[serde(default)]
    pub enable_compression: bool,

    /// 1 = enabled, 0 = disabled.
    #[serde(default = "default_status")]
    pub status: u8,

    /// Plugin configs (kept for compatibility, we map relevant ones).
    #[serde(default)]
    pub plugins: Option<serde_json::Value>,
}

/// Header matching condition for a route.
///
/// Supports exact match (default), prefix match, regex match, and
/// presence-only check. Multiple matchers on a route use AND semantics.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeaderMatcher {
    /// Header name (case-insensitive at match time).
    pub name: String,

    /// Expected value. Ignored when `match_type` is "present".
    #[serde(default)]
    pub value: String,

    /// Match type: "exact" (default), "prefix", "regex", "present".
    /// - "exact": header value must equal `value` exactly.
    /// - "prefix": header value must start with `value`.
    /// - "regex": header value must match `value` as a regex pattern.
    /// - "present": header just needs to exist, `value` is ignored.
    #[serde(default = "default_header_match_type")]
    pub match_type: String,

    /// If true, the condition is inverted (header must NOT match).
    #[serde(default)]
    pub invert: bool,
}

fn default_header_match_type() -> String {
    "exact".to_string()
}

/// A single header transformation rule.
///
/// Operations:
/// - `"set"` — set the header to `value`, replacing any existing value.
/// - `"add"` — append `value` to the header (allows multiple values).
/// - `"remove"` — remove the header entirely (`value` is ignored).
///
/// Applied at request or response phase depending on which list it belongs to.
/// Use request transforms for traffic coloring (e.g. `X-Env: canary`),
/// and response transforms for exposing debug info or stripping internal headers.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeaderTransform {
    /// Header name.
    pub name: String,

    /// Header value. Ignored when `action` is "remove".
    #[serde(default)]
    pub value: String,

    /// Action: "set" (default), "add", "remove".
    #[serde(default = "default_header_transform_action")]
    pub action: String,
}

fn default_header_transform_action() -> String {
    "set".to_string()
}

/// A weighted reference to a cluster. The route selects a cluster
/// proportional to its weight before delegating to the cluster's LB.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WeightedCluster {
    /// Cluster name (must match a `ClusterConfig.name`).
    pub name: String,

    /// Relative weight for traffic distribution.
    #[serde(default = "default_cluster_weight")]
    pub weight: u32,
}

fn default_cluster_weight() -> u32 {
    100
}

fn default_status() -> u8 {
    1
}

/// An independent cluster (upstream) definition.
///
/// Clusters own all upstream-related concerns: nodes, LB policy, timeouts,
/// health checks, circuit breakers, retries. Multiple routes can reference
/// the same cluster, and a single route can split traffic across clusters.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ClusterConfig {
    /// Unique cluster name.
    pub name: String,

    /// Load balancing type: "roundrobin", "random", "least_request", "peak_ewma".
    #[serde(rename = "type", default = "default_upstream_type")]
    pub lb_type: String,

    /// Connection timeout settings (seconds).
    #[serde(default)]
    pub timeout: TimeoutConfig,

    /// HTTP scheme for upstream.
    #[serde(default = "default_scheme")]
    pub scheme: String,

    /// Pass host mode: "pass" (use client host), "node" (use upstream host), "rewrite" + upstream_host.
    #[serde(default = "default_pass_host")]
    pub pass_host: String,

    /// Upstream host to use when pass_host is "rewrite".
    #[serde(default)]
    pub upstream_host: Option<String>,

    /// Static upstream nodes.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub nodes: Vec<UpstreamNode>,

    /// Consul service discovery.
    #[serde(default)]
    pub discovery_type: Option<String>,

    /// Service name for consul discovery.
    #[serde(default)]
    pub service_name: Option<String>,

    /// Consul discovery args.
    #[serde(default)]
    pub discovery_args: Option<DiscoveryArgs>,

    /// Keepalive pool settings.
    #[serde(default)]
    pub keepalive_pool: KeepalivePoolConfig,

    /// Health check configuration.
    #[serde(default)]
    pub health_check: Option<HealthCheckConfig>,

    /// Retry policy for failed upstream requests.
    #[serde(default)]
    pub retry: Option<RetryConfig>,

    /// Circuit breaker configuration per upstream node.
    #[serde(default)]
    pub circuit_breaker: Option<CircuitBreakerConfig>,

    /// Whether to verify upstream TLS certificates.
    /// Default is `false` — typical for gateway scenarios where upstreams are
    /// internal services using self-signed or private CA certificates.
    /// Set to `true` to enforce full certificate chain validation.
    #[serde(default)]
    pub tls_verify: bool,
}

fn default_upstream_type() -> String {
    "roundrobin".to_string()
}

fn default_scheme() -> String {
    "http".to_string()
}

fn default_pass_host() -> String {
    "pass".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TimeoutConfig {
    /// Connect timeout in seconds.
    #[serde(default = "default_timeout")]
    pub connect: f64,

    /// Send timeout in seconds.
    #[serde(default = "default_timeout")]
    pub send: f64,

    /// Read timeout in seconds.
    #[serde(default = "default_timeout")]
    pub read: f64,
}

impl Default for TimeoutConfig {
    fn default() -> Self {
        Self {
            connect: default_timeout(),
            send: default_timeout(),
            read: default_timeout(),
        }
    }
}

fn default_timeout() -> f64 {
    6.0
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpstreamNode {
    pub host: String,
    pub port: u16,
    #[serde(default = "default_weight")]
    pub weight: u32,
    /// Optional metadata for subset matching.
    #[serde(default)]
    pub metadata: HashMap<String, String>,
}

fn default_weight() -> u32 {
    100
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DiscoveryArgs {
    /// Metadata match filters for consul service discovery.
    #[serde(default)]
    pub metadata_match: HashMap<String, Vec<String>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct KeepalivePoolConfig {
    /// Idle timeout in seconds.
    #[serde(default = "default_idle_timeout")]
    pub idle_timeout: u64,

    /// Max requests per connection.
    #[serde(default = "default_requests")]
    pub requests: u64,

    /// Pool size.
    #[serde(default = "default_pool_size")]
    pub size: usize,
}

impl Default for KeepalivePoolConfig {
    fn default() -> Self {
        Self {
            idle_timeout: default_idle_timeout(),
            requests: default_requests(),
            size: default_pool_size(),
        }
    }
}

fn default_idle_timeout() -> u64 {
    60
}

fn default_requests() -> u64 {
    1000
}

fn default_pool_size() -> usize {
    320
}

/// Rate limiting configuration.
/// Uses a per-core token bucket to avoid cross-core contention.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RateLimitConfig {
    /// Rate limit mode: "req" (request rate) or "count" (fixed window count).
    #[serde(default = "default_limit_mode")]
    pub mode: String,

    /// Requests per second (for mode "req").
    #[serde(default)]
    pub rate: Option<f64>,

    /// Burst capacity (for mode "req").
    #[serde(default)]
    pub burst: Option<u64>,

    /// Fixed window count limit (for mode "count").
    #[serde(default)]
    pub count: Option<u64>,

    /// Time window in seconds (for mode "count").
    #[serde(default)]
    pub time_window: Option<u64>,

    /// Key expression for rate limit grouping.
    /// Supported: "remote_addr", "host_uri" (concat host+uri), "uri".
    #[serde(default = "default_limit_key")]
    pub key: String,

    /// HTTP status code to return when rate limited.
    #[serde(default = "default_rejected_code")]
    pub rejected_code: u16,
}

fn default_limit_mode() -> String {
    "req".to_string()
}

fn default_limit_key() -> String {
    "host_uri".to_string()
}

fn default_rejected_code() -> u16 {
    429
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthCheckConfig {
    /// Active health check: send HTTP probes to upstream nodes.
    /// This is the only health check mode — passive health check has been
    /// removed in favour of the circuit breaker for real-traffic failure detection.
    #[serde(default)]
    pub active: Option<ActiveHealthCheck>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveHealthCheck {
    /// Health check interval in seconds.
    #[serde(default = "default_hc_interval")]
    pub interval: u64,

    /// Health check path.
    #[serde(default = "default_hc_path")]
    pub path: String,

    /// Optional override port for health check probes.
    /// When set, probes are sent to this port instead of the node's business port.
    /// Useful when the health endpoint runs on a separate management port.
    #[serde(default)]
    pub port: Option<u16>,

    /// Expected healthy HTTP status codes.
    #[serde(default = "default_healthy_statuses")]
    pub healthy_statuses: Vec<u16>,

    /// Number of consecutive successes to mark healthy.
    #[serde(default = "default_hc_threshold")]
    pub healthy_threshold: u32,

    /// Number of consecutive failures to mark unhealthy.
    #[serde(default = "default_hc_threshold")]
    pub unhealthy_threshold: u32,

    /// Timeout for health check request in seconds.
    #[serde(default = "default_hc_timeout")]
    pub timeout: u64,

    /// Maximum number of concurrent health check probes per cluster.
    /// Prevents probe storms when a cluster has thousands of instances.
    #[serde(default = "default_hc_concurrency")]
    pub concurrency: usize,
}

fn default_hc_interval() -> u64 {
    10
}

fn default_hc_path() -> String {
    "/health".to_string()
}

fn default_healthy_statuses() -> Vec<u16> {
    vec![200]
}

fn default_hc_threshold() -> u32 {
    3
}

fn default_hc_timeout() -> u64 {
    3
}

fn default_hc_concurrency() -> usize {
    64
}

/// Self-registration configuration for registering this gateway to Consul.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RegistrationConfig {
    /// Whether self-registration is enabled.
    #[serde(default)]
    pub enabled: bool,

    /// Service name to register as.
    #[serde(default = "default_registration_service_name")]
    pub service_name: String,

    /// TTL for the health check in seconds.
    #[serde(default = "default_ttl_secs")]
    pub ttl_secs: u64,

    /// Deregister critical service after this many seconds.
    #[serde(default = "default_deregister_after_secs")]
    pub deregister_after_secs: u64,

    /// Metadata to attach to the registered service.
    #[serde(default)]
    pub metadata: HashMap<String, String>,
}

impl Default for RegistrationConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            service_name: default_registration_service_name(),
            ttl_secs: default_ttl_secs(),
            deregister_after_secs: default_deregister_after_secs(),
            metadata: HashMap::new(),
        }
    }
}

fn default_registration_service_name() -> String {
    "hermes-gateway".to_string()
}

fn default_ttl_secs() -> u64 {
    30
}

fn default_deregister_after_secs() -> u64 {
    60
}

/// Retry policy for failed upstream requests.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetryConfig {
    /// Maximum number of retry attempts (excluding the initial request).
    #[serde(default = "default_retry_count")]
    pub count: u32,

    /// HTTP status codes that trigger a retry.
    #[serde(default = "default_retry_statuses")]
    pub retry_on_statuses: Vec<u16>,

    /// Whether to retry on connection errors.
    #[serde(default = "default_true")]
    pub retry_on_connect_failure: bool,

    /// Whether to retry on timeouts.
    #[serde(default = "default_true")]
    pub retry_on_timeout: bool,
}

fn default_retry_count() -> u32 {
    2
}

fn default_retry_statuses() -> Vec<u16> {
    vec![502, 503, 504]
}

fn default_true() -> bool {
    true
}

/// Circuit breaker configuration per upstream node.
///
/// State machine: Closed → Open → HalfOpen → Closed/Open
/// - Closed: requests flow normally, failures are counted
/// - Open: all requests are rejected immediately (fast-fail)
/// - HalfOpen: a single probe request is allowed through;
///   success → Closed, failure → Open
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CircuitBreakerConfig {
    /// Number of consecutive failures to trip the breaker (Closed → Open).
    #[serde(default = "default_cb_failure_threshold")]
    pub failure_threshold: u32,

    /// Number of consecutive successes in HalfOpen to close the breaker.
    #[serde(default = "default_cb_success_threshold")]
    pub success_threshold: u32,

    /// How long (seconds) the breaker stays Open before transitioning to HalfOpen.
    #[serde(default = "default_cb_open_duration")]
    pub open_duration_secs: u64,
}

fn default_cb_failure_threshold() -> u32 {
    5
}

fn default_cb_success_threshold() -> u32 {
    2
}

fn default_cb_open_duration() -> u64 {
    30
}

/// Instance registry configuration for distributed rate limiting.
///
/// Each gateway instance registers itself under a shared etcd prefix with a lease.
/// All instances watch this prefix to know the total peer count, then divide
/// the configured rate/count limits evenly. This achieves decentralized, eventually-
/// consistent distributed rate limiting without a central counter.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InstanceRegistryConfig {
    /// Whether distributed instance registry is enabled.
    #[serde(default)]
    pub enabled: bool,

    /// etcd key prefix for instance registration.
    #[serde(default = "default_instance_prefix")]
    pub prefix: String,

    /// Lease TTL in seconds. The instance key auto-expires if keepalive stops.
    #[serde(default = "default_instance_lease_ttl")]
    pub lease_ttl_secs: u64,
}

impl Default for InstanceRegistryConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            prefix: default_instance_prefix(),
            lease_ttl_secs: default_instance_lease_ttl(),
        }
    }
}

fn default_instance_prefix() -> String {
    "/hermes/instances".to_string()
}

fn default_instance_lease_ttl() -> u64 {
    15
}
