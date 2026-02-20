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
    #[serde(default)]
    pub consul: ConsulConfig,

    #[serde(default)]
    pub etcd: EtcdConfig,

    /// Self-registration to Consul so upstream gateways can discover us.
    #[serde(default)]
    pub registration: RegistrationConfig,

    /// Gateways register themselves in etcd and track peer count to split
    /// rate limits evenly across instances.
    #[serde(default)]
    pub instance_registry: InstanceRegistryConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConsulConfig {
    #[serde(default = "default_consul_addr")]
    pub address: String,

    #[serde(default)]
    pub datacenter: Option<String>,

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
    #[serde(default = "default_etcd_endpoints")]
    pub endpoints: Vec<String>,

    #[serde(default = "default_etcd_domain_prefix")]
    pub domain_prefix: String,

    #[serde(default = "default_etcd_cluster_prefix")]
    pub cluster_prefix: String,

    /// etcd key prefix for controller metadata (e.g. config_revision).
    #[serde(default)]
    pub meta_prefix: Option<String>,

    #[serde(default)]
    pub username: Option<String>,

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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DomainConfig {
    pub name: String,

    /// Host patterns. Supports exact (`api.example.com`),
    /// wildcard suffix (`*.example.com`), wildcard prefix (`api.*`).
    pub hosts: Vec<String>,

    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub routes: Vec<RouteConfig>,
}

/// Routes reference `ClusterConfig` entries by name with weights,
/// enabling canary / blue-green / traffic-split at the routing layer.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RouteConfig {
    #[serde(default)]
    pub id: String,

    #[serde(default)]
    pub name: String,

    /// URI pattern. Supports exact match, prefix match (`/v1/api/*`), and `/*` for catch-all.
    pub uri: String,

    /// Allowed HTTP methods. Empty means all methods.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub methods: Vec<String>,

    /// Header matchers (AND semantics).
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub headers: Vec<HeaderMatcher>,

    /// Higher value = higher priority.
    #[serde(default)]
    pub priority: i32,

    /// Weighted cluster references for traffic distribution.
    pub clusters: Vec<WeightedCluster>,

    #[serde(default)]
    pub rate_limit: Option<RateLimitConfig>,

    /// When set, the request header value overrides weighted cluster selection.
    #[serde(default)]
    pub cluster_override_header: Option<String>,

    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub request_header_transforms: Vec<HeaderTransform>,

    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub response_header_transforms: Vec<HeaderTransform>,

    /// Requests exceeding this limit are rejected with 413. `None` means no limit.
    #[serde(default)]
    pub max_body_bytes: Option<u64>,

    #[serde(default)]
    pub enable_compression: bool,

    /// 1 = enabled, 0 = disabled.
    #[serde(default = "default_status")]
    pub status: u8,

    #[serde(default)]
    pub plugins: Option<serde_json::Value>,
}

/// Supports exact (default), prefix, regex, and presence-only match.
/// Multiple matchers on a route use AND semantics.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeaderMatcher {
    pub name: String,

    /// Ignored when `match_type` is "present".
    #[serde(default)]
    pub value: String,

    /// "exact" (default), "prefix", "regex", "present".
    #[serde(default = "default_header_match_type")]
    pub match_type: String,

    #[serde(default)]
    pub invert: bool,
}

fn default_header_match_type() -> String {
    "exact".to_string()
}

/// Operations: "set" (replace), "add" (append), "remove" (delete).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeaderTransform {
    pub name: String,

    #[serde(default)]
    pub value: String,

    /// "set" (default), "add", "remove".
    #[serde(default = "default_header_transform_action")]
    pub action: String,
}

fn default_header_transform_action() -> String {
    "set".to_string()
}

/// Weighted reference to a cluster for traffic splitting.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WeightedCluster {
    pub name: String,

    #[serde(default = "default_cluster_weight")]
    pub weight: u32,
}

fn default_cluster_weight() -> u32 {
    100
}

fn default_status() -> u8 {
    1
}

/// Cluster (upstream) definition. Owns nodes, LB policy, timeouts,
/// health checks, circuit breakers, retries.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ClusterConfig {
    pub name: String,

    /// "roundrobin", "random", "least_request", "peak_ewma".
    #[serde(rename = "type", default = "default_upstream_type")]
    pub lb_type: String,

    #[serde(default)]
    pub timeout: TimeoutConfig,

    #[serde(default = "default_scheme")]
    pub scheme: String,

    /// "pass" (use client host), "node" (use upstream host), "rewrite" + upstream_host.
    #[serde(default = "default_pass_host")]
    pub pass_host: String,

    #[serde(default)]
    pub upstream_host: Option<String>,

    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub nodes: Vec<UpstreamNode>,

    #[serde(default)]
    pub discovery_type: Option<String>,

    #[serde(default)]
    pub service_name: Option<String>,

    #[serde(default)]
    pub discovery_args: Option<DiscoveryArgs>,

    #[serde(default)]
    pub keepalive_pool: KeepalivePoolConfig,

    #[serde(default)]
    pub health_check: Option<HealthCheckConfig>,

    #[serde(default)]
    pub retry: Option<RetryConfig>,

    #[serde(default)]
    pub circuit_breaker: Option<CircuitBreakerConfig>,

    /// Default `false` — typical for internal services with self-signed certs.
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
    #[serde(default = "default_timeout")]
    pub connect: f64,

    #[serde(default = "default_timeout")]
    pub send: f64,

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
    #[serde(default)]
    pub metadata: HashMap<String, String>,
}

fn default_weight() -> u32 {
    100
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DiscoveryArgs {
    #[serde(default)]
    pub metadata_match: HashMap<String, Vec<String>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct KeepalivePoolConfig {
    #[serde(default = "default_idle_timeout")]
    pub idle_timeout: u64,

    #[serde(default = "default_requests")]
    pub requests: u64,

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

/// Per-core token bucket to avoid cross-core contention.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RateLimitConfig {
    /// "req" (request rate) or "count" (fixed window count).
    #[serde(default = "default_limit_mode")]
    pub mode: String,

    #[serde(default)]
    pub rate: Option<f64>,

    #[serde(default)]
    pub burst: Option<u64>,

    #[serde(default)]
    pub count: Option<u64>,

    #[serde(default)]
    pub time_window: Option<u64>,

    /// "remote_addr", "host_uri", "uri".
    #[serde(default = "default_limit_key")]
    pub key: String,

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
    #[serde(default)]
    pub active: Option<ActiveHealthCheck>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveHealthCheck {
    #[serde(default = "default_hc_interval")]
    pub interval: u64,

    #[serde(default = "default_hc_path")]
    pub path: String,

    /// Override port for probes (when health endpoint runs on a separate port).
    #[serde(default)]
    pub port: Option<u16>,

    #[serde(default = "default_healthy_statuses")]
    pub healthy_statuses: Vec<u16>,

    #[serde(default = "default_hc_threshold")]
    pub healthy_threshold: u32,

    #[serde(default = "default_hc_threshold")]
    pub unhealthy_threshold: u32,

    #[serde(default = "default_hc_timeout")]
    pub timeout: u64,

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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RegistrationConfig {
    #[serde(default)]
    pub enabled: bool,

    #[serde(default = "default_registration_service_name")]
    pub service_name: String,

    #[serde(default = "default_ttl_secs")]
    pub ttl_secs: u64,

    #[serde(default = "default_deregister_after_secs")]
    pub deregister_after_secs: u64,

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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetryConfig {
    #[serde(default = "default_retry_count")]
    pub count: u32,

    #[serde(default = "default_retry_statuses")]
    pub retry_on_statuses: Vec<u16>,

    #[serde(default = "default_true")]
    pub retry_on_connect_failure: bool,

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

/// State machine: Closed → Open → HalfOpen → Closed/Open.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CircuitBreakerConfig {
    #[serde(default = "default_cb_failure_threshold")]
    pub failure_threshold: u32,

    #[serde(default = "default_cb_success_threshold")]
    pub success_threshold: u32,

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

/// Each gateway registers under a shared etcd prefix with a lease.
/// All instances watch this prefix to know total peer count, then divide
/// rate/count limits evenly for decentralized distributed rate limiting.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InstanceRegistryConfig {
    #[serde(default)]
    pub enabled: bool,

    #[serde(default = "default_instance_prefix")]
    pub prefix: String,

    /// Lease TTL in seconds. Auto-expires if keepalive stops.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_domain_full_serde() {
        let json = r#"{
            "name": "user-service",
            "hosts": ["api.example.com", "*.staging.example.com"],
            "routes": [
                {
                    "id": "r1",
                    "name": "users-api",
                    "uri": "/v1/users/*",
                    "methods": ["GET", "POST"],
                    "headers": [
                        {"name": "X-Canary", "value": "true", "match_type": "exact", "invert": false}
                    ],
                    "priority": 10,
                    "clusters": [
                        {"name": "prod", "weight": 90},
                        {"name": "canary", "weight": 10}
                    ],
                    "rate_limit": {
                        "mode": "req",
                        "rate": 1000.0,
                        "burst": 200,
                        "key": "remote_addr",
                        "rejected_code": 429
                    },
                    "cluster_override_header": "X-Override",
                    "request_header_transforms": [
                        {"name": "X-Env", "value": "canary", "action": "set"}
                    ],
                    "response_header_transforms": [
                        {"name": "X-Debug", "value": "", "action": "remove"}
                    ],
                    "max_body_bytes": 1048576,
                    "enable_compression": true,
                    "status": 1
                }
            ]
        }"#;

        let domain: DomainConfig = serde_json::from_str(json).unwrap();
        assert_eq!(domain.name, "user-service");
        assert_eq!(domain.hosts.len(), 2);
        assert_eq!(domain.hosts[0], "api.example.com");
        assert_eq!(domain.hosts[1], "*.staging.example.com");
        assert_eq!(domain.routes.len(), 1);

        let route = &domain.routes[0];
        assert_eq!(route.id, "r1");
        assert_eq!(route.name, "users-api");
        assert_eq!(route.uri, "/v1/users/*");
        assert_eq!(route.methods, vec!["GET", "POST"]);
        assert_eq!(route.priority, 10);
        assert_eq!(route.status, 1);
        assert_eq!(route.max_body_bytes, Some(1048576));
        assert!(route.enable_compression);
        assert_eq!(
            route.cluster_override_header,
            Some("X-Override".to_string())
        );

        assert_eq!(route.clusters.len(), 2);
        assert_eq!(route.clusters[0].name, "prod");
        assert_eq!(route.clusters[0].weight, 90);
        assert_eq!(route.clusters[1].name, "canary");
        assert_eq!(route.clusters[1].weight, 10);

        let rl = route.rate_limit.as_ref().unwrap();
        assert_eq!(rl.mode, "req");
        assert_eq!(rl.rate, Some(1000.0));
        assert_eq!(rl.burst, Some(200));
        assert_eq!(rl.key, "remote_addr");
        assert_eq!(rl.rejected_code, 429);

        assert_eq!(route.headers.len(), 1);
        assert_eq!(route.headers[0].name, "X-Canary");
        assert_eq!(route.headers[0].value, "true");
        assert_eq!(route.headers[0].match_type, "exact");
        assert!(!route.headers[0].invert);

        assert_eq!(route.request_header_transforms.len(), 1);
        assert_eq!(route.request_header_transforms[0].name, "X-Env");
        assert_eq!(route.request_header_transforms[0].value, "canary");
        assert_eq!(route.request_header_transforms[0].action, "set");

        assert_eq!(route.response_header_transforms.len(), 1);
        assert_eq!(route.response_header_transforms[0].name, "X-Debug");
        assert_eq!(route.response_header_transforms[0].action, "remove");
    }

    #[test]
    fn test_domain_minimal_defaults() {
        let json = r#"{
            "name": "minimal",
            "hosts": ["example.com"],
            "routes": [
                {
                    "uri": "/",
                    "clusters": [{"name": "backend"}]
                }
            ]
        }"#;

        let domain: DomainConfig = serde_json::from_str(json).unwrap();
        assert_eq!(domain.name, "minimal");

        let route = &domain.routes[0];
        assert_eq!(route.id, "");
        assert_eq!(route.name, "");
        assert!(route.methods.is_empty());
        assert!(route.headers.is_empty());
        assert_eq!(route.priority, 0);
        assert_eq!(route.status, 1);
        assert!(route.rate_limit.is_none());
        assert!(route.cluster_override_header.is_none());
        assert!(route.request_header_transforms.is_empty());
        assert!(route.response_header_transforms.is_empty());
        assert!(route.max_body_bytes.is_none());
        assert!(!route.enable_compression);
        assert_eq!(route.clusters[0].weight, 100);
    }

    #[test]
    fn test_domain_null_routes_defaults_to_empty() {
        let json = r#"{"name": "no-routes", "hosts": ["h.com"], "routes": null}"#;
        let domain: DomainConfig = serde_json::from_str(json).unwrap();
        assert!(domain.routes.is_empty());
    }

    #[test]
    fn test_domain_missing_routes_defaults_to_empty() {
        let json = r#"{"name": "no-routes", "hosts": ["h.com"]}"#;
        let domain: DomainConfig = serde_json::from_str(json).unwrap();
        assert!(domain.routes.is_empty());
    }

    #[test]
    fn test_cluster_defaults() {
        let json = r#"{"name": "default-cluster"}"#;
        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        assert_eq!(cluster.name, "default-cluster");
        assert_eq!(cluster.lb_type, "roundrobin");
        assert_eq!(cluster.scheme, "http");
        assert_eq!(cluster.pass_host, "pass");
        assert!(cluster.upstream_host.is_none());
        assert!(cluster.nodes.is_empty());
        assert!(cluster.discovery_type.is_none());
        assert!(cluster.service_name.is_none());
        assert!(cluster.discovery_args.is_none());
        assert!(cluster.health_check.is_none());
        assert!(cluster.retry.is_none());
        assert!(cluster.circuit_breaker.is_none());
        assert!(!cluster.tls_verify);
        assert_eq!(cluster.timeout.connect, 6.0);
        assert_eq!(cluster.timeout.send, 6.0);
        assert_eq!(cluster.timeout.read, 6.0);
        assert_eq!(cluster.keepalive_pool.idle_timeout, 60);
        assert_eq!(cluster.keepalive_pool.requests, 1000);
        assert_eq!(cluster.keepalive_pool.size, 320);
    }

    #[test]
    fn test_cluster_with_discovery() {
        let json = r#"{
            "name": "consul-svc",
            "discovery_type": "consul",
            "service_name": "my-service",
            "discovery_args": {
                "metadata_match": {
                    "namespace": ["prod", "canary"],
                    "region": ["us-east-1"]
                }
            }
        }"#;

        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        assert_eq!(cluster.discovery_type, Some("consul".to_string()));
        assert_eq!(cluster.service_name, Some("my-service".to_string()));
        let args = cluster.discovery_args.as_ref().unwrap();
        assert_eq!(args.metadata_match.len(), 2);
        assert_eq!(args.metadata_match["namespace"], vec!["prod", "canary"]);
        assert_eq!(args.metadata_match["region"], vec!["us-east-1"]);
    }

    #[test]
    fn test_cluster_with_health_check() {
        let json = r#"{
            "name": "hc-cluster",
            "health_check": {
                "active": {
                    "interval": 5,
                    "path": "/healthz",
                    "port": 8081,
                    "healthy_statuses": [200, 204],
                    "healthy_threshold": 2,
                    "unhealthy_threshold": 5,
                    "timeout": 2,
                    "concurrency": 32
                }
            }
        }"#;

        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        let hc = cluster.health_check.unwrap();
        let active = hc.active.unwrap();
        assert_eq!(active.interval, 5);
        assert_eq!(active.path, "/healthz");
        assert_eq!(active.port, Some(8081));
        assert_eq!(active.healthy_statuses, vec![200, 204]);
        assert_eq!(active.healthy_threshold, 2);
        assert_eq!(active.unhealthy_threshold, 5);
        assert_eq!(active.timeout, 2);
        assert_eq!(active.concurrency, 32);
    }

    #[test]
    fn test_health_check_defaults() {
        let json = r#"{
            "name": "hc-defaults",
            "health_check": { "active": {} }
        }"#;

        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        let active = cluster.health_check.unwrap().active.unwrap();
        assert_eq!(active.interval, 10);
        assert_eq!(active.path, "/health");
        assert!(active.port.is_none());
        assert_eq!(active.healthy_statuses, vec![200]);
        assert_eq!(active.healthy_threshold, 3);
        assert_eq!(active.unhealthy_threshold, 3);
        assert_eq!(active.timeout, 3);
        assert_eq!(active.concurrency, 64);
    }

    #[test]
    fn test_cluster_with_retry() {
        let json = r#"{
            "name": "retry-cluster",
            "retry": {
                "count": 3,
                "retry_on_statuses": [502, 503],
                "retry_on_connect_failure": false,
                "retry_on_timeout": true
            }
        }"#;

        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        let retry = cluster.retry.unwrap();
        assert_eq!(retry.count, 3);
        assert_eq!(retry.retry_on_statuses, vec![502, 503]);
        assert!(!retry.retry_on_connect_failure);
        assert!(retry.retry_on_timeout);
    }

    #[test]
    fn test_retry_defaults() {
        let json = r#"{"count": 1}"#;
        let retry: RetryConfig = serde_json::from_str(json).unwrap();
        assert_eq!(retry.count, 1);
        assert_eq!(retry.retry_on_statuses, vec![502, 503, 504]);
        assert!(retry.retry_on_connect_failure);
        assert!(retry.retry_on_timeout);
    }

    #[test]
    fn test_circuit_breaker_defaults() {
        let json = r#"{}"#;
        let cb: CircuitBreakerConfig = serde_json::from_str(json).unwrap();
        assert_eq!(cb.failure_threshold, 5);
        assert_eq!(cb.success_threshold, 2);
        assert_eq!(cb.open_duration_secs, 30);
    }

    #[test]
    fn test_cluster_with_tls_verify() {
        let json = r#"{"name": "tls", "scheme": "https", "tls_verify": true}"#;
        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        assert_eq!(cluster.scheme, "https");
        assert!(cluster.tls_verify);
    }

    #[test]
    fn test_upstream_node_defaults() {
        let json = r#"{"host": "10.0.0.1", "port": 8080}"#;
        let node: UpstreamNode = serde_json::from_str(json).unwrap();
        assert_eq!(node.host, "10.0.0.1");
        assert_eq!(node.port, 8080);
        assert_eq!(node.weight, 100);
        assert!(node.metadata.is_empty());
    }

    #[test]
    fn test_upstream_node_with_metadata() {
        let json = r#"{"host": "10.0.0.1", "port": 8080, "weight": 50, "metadata": {"env": "prod", "zone": "a"}}"#;
        let node: UpstreamNode = serde_json::from_str(json).unwrap();
        assert_eq!(node.weight, 50);
        assert_eq!(node.metadata.len(), 2);
        assert_eq!(node.metadata["env"], "prod");
        assert_eq!(node.metadata["zone"], "a");
    }

    #[test]
    fn test_null_methods_defaults_to_empty() {
        let json = r#"{"uri": "/", "methods": null, "clusters": [{"name": "x"}]}"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.methods.is_empty());
    }

    #[test]
    fn test_null_headers_defaults_to_empty() {
        let json = r#"{"uri": "/", "headers": null, "clusters": [{"name": "x"}]}"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.headers.is_empty());
    }

    #[test]
    fn test_null_nodes_defaults_to_empty() {
        let json = r#"{"name": "c", "nodes": null}"#;
        let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
        assert!(cluster.nodes.is_empty());
    }

    #[test]
    fn test_null_request_transforms_defaults_to_empty() {
        let json =
            r#"{"uri": "/", "request_header_transforms": null, "clusters": [{"name": "x"}]}"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.request_header_transforms.is_empty());
    }

    #[test]
    fn test_null_response_transforms_defaults_to_empty() {
        let json =
            r#"{"uri": "/", "response_header_transforms": null, "clusters": [{"name": "x"}]}"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.response_header_transforms.is_empty());
    }

    #[test]
    fn test_header_matcher_all_types() {
        for (match_type, invert) in &[
            ("exact", false),
            ("prefix", true),
            ("regex", false),
            ("present", false),
        ] {
            let json = format!(
                r#"{{"name": "X-Test", "value": "v", "match_type": "{}", "invert": {}}}"#,
                match_type, invert
            );
            let hm: HeaderMatcher = serde_json::from_str(&json).unwrap();
            assert_eq!(hm.match_type, *match_type);
            assert_eq!(hm.invert, *invert);
        }
    }

    #[test]
    fn test_header_transform_defaults() {
        let json = r#"{"name": "X-Custom"}"#;
        let ht: HeaderTransform = serde_json::from_str(json).unwrap();
        assert_eq!(ht.name, "X-Custom");
        assert_eq!(ht.value, "");
        assert_eq!(ht.action, "set");
    }

    #[test]
    fn test_header_transform_all_actions() {
        for action in &["set", "add", "remove"] {
            let json = format!(r#"{{"name": "H", "value": "V", "action": "{}"}}"#, action);
            let ht: HeaderTransform = serde_json::from_str(&json).unwrap();
            assert_eq!(ht.action, *action);
        }
    }

    #[test]
    fn test_rate_limit_defaults() {
        let json = r#"{}"#;
        let rl: RateLimitConfig = serde_json::from_str(json).unwrap();
        assert_eq!(rl.mode, "req");
        assert_eq!(rl.key, "host_uri");
        assert_eq!(rl.rejected_code, 429);
        assert!(rl.rate.is_none());
        assert!(rl.burst.is_none());
        assert!(rl.count.is_none());
        assert!(rl.time_window.is_none());
    }

    #[test]
    fn test_rate_limit_count_mode() {
        let json = r#"{"mode": "count", "count": 500, "time_window": 60, "key": "route"}"#;
        let rl: RateLimitConfig = serde_json::from_str(json).unwrap();
        assert_eq!(rl.mode, "count");
        assert_eq!(rl.count, Some(500));
        assert_eq!(rl.time_window, Some(60));
        assert_eq!(rl.key, "route");
    }

    #[test]
    fn test_weighted_cluster_default_weight() {
        let json = r#"{"name": "backend"}"#;
        let wc: WeightedCluster = serde_json::from_str(json).unwrap();
        assert_eq!(wc.name, "backend");
        assert_eq!(wc.weight, 100);
    }

    #[test]
    fn test_keepalive_pool_defaults() {
        let kp = KeepalivePoolConfig::default();
        assert_eq!(kp.idle_timeout, 60);
        assert_eq!(kp.requests, 1000);
        assert_eq!(kp.size, 320);
    }

    #[test]
    fn test_keepalive_pool_custom() {
        let json = r#"{"idle_timeout": 30, "requests": 500, "size": 64}"#;
        let kp: KeepalivePoolConfig = serde_json::from_str(json).unwrap();
        assert_eq!(kp.idle_timeout, 30);
        assert_eq!(kp.requests, 500);
        assert_eq!(kp.size, 64);
    }

    #[test]
    fn test_timeout_defaults() {
        let tc = TimeoutConfig::default();
        assert_eq!(tc.connect, 6.0);
        assert_eq!(tc.send, 6.0);
        assert_eq!(tc.read, 6.0);
    }

    #[test]
    fn test_timeout_custom() {
        let json = r#"{"connect": 1.5, "send": 3.0, "read": 10.0}"#;
        let tc: TimeoutConfig = serde_json::from_str(json).unwrap();
        assert_eq!(tc.connect, 1.5);
        assert_eq!(tc.send, 3.0);
        assert_eq!(tc.read, 10.0);
    }

    #[test]
    fn test_gateway_config_defaults() {
        let cfg = GatewayConfig::default();
        assert_eq!(cfg.consul.address, "http://127.0.0.1:8500");
        assert_eq!(cfg.consul.poll_interval_secs, 10);
        assert!(cfg.consul.datacenter.is_none());
        assert!(cfg.consul.token.is_none());

        assert_eq!(cfg.etcd.endpoints, vec!["http://127.0.0.1:2379"]);
        assert_eq!(cfg.etcd.domain_prefix, "/hermes/domains");
        assert_eq!(cfg.etcd.cluster_prefix, "/hermes/clusters");
        assert!(cfg.etcd.meta_prefix.is_none());
        assert!(cfg.etcd.username.is_none());
        assert!(cfg.etcd.password.is_none());

        assert!(!cfg.registration.enabled);
        assert_eq!(cfg.registration.service_name, "hermes-gateway");
        assert_eq!(cfg.registration.ttl_secs, 30);
        assert_eq!(cfg.registration.deregister_after_secs, 60);
        assert!(cfg.registration.metadata.is_empty());

        assert!(!cfg.instance_registry.enabled);
        assert_eq!(cfg.instance_registry.prefix, "/hermes/instances");
        assert_eq!(cfg.instance_registry.lease_ttl_secs, 15);
    }

    #[test]
    fn test_registration_config_full() {
        let json = r#"{
            "enabled": true,
            "service_name": "my-gw",
            "ttl_secs": 15,
            "deregister_after_secs": 120,
            "metadata": {"version": "1.0", "env": "prod"}
        }"#;
        let reg: RegistrationConfig = serde_json::from_str(json).unwrap();
        assert!(reg.enabled);
        assert_eq!(reg.service_name, "my-gw");
        assert_eq!(reg.ttl_secs, 15);
        assert_eq!(reg.deregister_after_secs, 120);
        assert_eq!(reg.metadata.len(), 2);
        assert_eq!(reg.metadata["version"], "1.0");
    }

    #[test]
    fn test_instance_registry_config() {
        let json = r#"{"enabled": true, "prefix": "/my/instances", "lease_ttl_secs": 30}"#;
        let ir: InstanceRegistryConfig = serde_json::from_str(json).unwrap();
        assert!(ir.enabled);
        assert_eq!(ir.prefix, "/my/instances");
        assert_eq!(ir.lease_ttl_secs, 30);
    }

    #[test]
    fn test_cluster_roundtrip() {
        let cluster = ClusterConfig {
            name: "roundtrip".to_string(),
            lb_type: "peak_ewma".to_string(),
            scheme: "https".to_string(),
            pass_host: "rewrite".to_string(),
            upstream_host: Some("api.internal".to_string()),
            tls_verify: true,
            timeout: TimeoutConfig {
                connect: 2.0,
                send: 5.0,
                read: 10.0,
            },
            nodes: vec![UpstreamNode {
                host: "10.0.0.1".to_string(),
                port: 8080,
                weight: 50,
                metadata: [("env".to_string(), "prod".to_string())]
                    .into_iter()
                    .collect(),
            }],
            keepalive_pool: KeepalivePoolConfig {
                idle_timeout: 30,
                requests: 500,
                size: 64,
            },
            retry: Some(RetryConfig {
                count: 3,
                retry_on_statuses: vec![502, 503],
                retry_on_connect_failure: true,
                retry_on_timeout: false,
            }),
            circuit_breaker: Some(CircuitBreakerConfig {
                failure_threshold: 10,
                success_threshold: 3,
                open_duration_secs: 60,
            }),
            ..ClusterConfig::default()
        };

        let serialized = serde_json::to_string(&cluster).unwrap();
        let deserialized: ClusterConfig = serde_json::from_str(&serialized).unwrap();

        assert_eq!(deserialized.name, "roundtrip");
        assert_eq!(deserialized.lb_type, "peak_ewma");
        assert_eq!(deserialized.scheme, "https");
        assert!(deserialized.tls_verify);
        assert_eq!(deserialized.nodes.len(), 1);
        assert_eq!(deserialized.nodes[0].metadata["env"], "prod");
        assert_eq!(deserialized.retry.unwrap().count, 3);
        assert_eq!(deserialized.circuit_breaker.unwrap().failure_threshold, 10);
    }

    #[test]
    fn test_consul_config_full() {
        let json = r#"{
            "address": "http://consul:8500",
            "datacenter": "dc1",
            "token": "secret",
            "poll_interval_secs": 30
        }"#;
        let cc: ConsulConfig = serde_json::from_str(json).unwrap();
        assert_eq!(cc.address, "http://consul:8500");
        assert_eq!(cc.datacenter, Some("dc1".to_string()));
        assert_eq!(cc.token, Some("secret".to_string()));
        assert_eq!(cc.poll_interval_secs, 30);
    }

    #[test]
    fn test_etcd_config_full() {
        let json = r#"{
            "endpoints": ["http://etcd1:2379", "http://etcd2:2379"],
            "domain_prefix": "/custom/domains",
            "cluster_prefix": "/custom/clusters",
            "meta_prefix": "/custom/meta",
            "username": "root",
            "password": "pass"
        }"#;
        let ec: EtcdConfig = serde_json::from_str(json).unwrap();
        assert_eq!(ec.endpoints.len(), 2);
        assert_eq!(ec.domain_prefix, "/custom/domains");
        assert_eq!(ec.cluster_prefix, "/custom/clusters");
        assert_eq!(ec.meta_prefix, Some("/custom/meta".to_string()));
        assert_eq!(ec.username, Some("root".to_string()));
        assert_eq!(ec.password, Some("pass".to_string()));
    }

    #[test]
    fn test_health_check_no_active() {
        let json = r#"{}"#;
        let hc: HealthCheckConfig = serde_json::from_str(json).unwrap();
        assert!(hc.active.is_none());
    }

    #[test]
    fn test_route_with_plugins() {
        let json = r#"{
            "uri": "/",
            "clusters": [{"name": "x"}],
            "plugins": {"cors": {"enabled": true}}
        }"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.plugins.is_some());
        let plugins = route.plugins.unwrap();
        assert!(plugins.get("cors").is_some());
    }

    #[test]
    fn test_route_without_plugins() {
        let json = r#"{"uri": "/", "clusters": [{"name": "x"}]}"#;
        let route: RouteConfig = serde_json::from_str(json).unwrap();
        assert!(route.plugins.is_none());
    }
}
