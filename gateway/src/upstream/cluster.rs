use crate::config::{ClusterConfig, KeepalivePoolConfig, UpstreamNode};
use crate::proxy::context::BoxBody;
use crate::upstream::circuit_breaker::CircuitBreakerRegistry;
use crate::upstream::loadbalance::{LoadBalancer, RequestGuard, UpstreamTarget};
use dashmap::DashMap;
use hyper_rustls::HttpsConnector;
use hyper_util::client::legacy::connect::HttpConnector;
use hyper_util::client::legacy::Client;
use hyper_util::rt::TokioExecutor;
use std::collections::HashSet;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;
use std::time::Duration;

/// A rustls `ServerCertVerifier` that accepts any certificate without validation.
/// Used when `tls_verify: false` — the common case for internal / mesh traffic
/// where encryption is desired but upstream identity verification is not.
#[derive(Debug)]
struct NoVerifier;

impl rustls::client::danger::ServerCertVerifier for NoVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &rustls::pki_types::CertificateDer<'_>,
        _intermediates: &[rustls::pki_types::CertificateDer<'_>],
        _server_name: &rustls::pki_types::ServerName<'_>,
        _ocsp_response: &[u8],
        _now: rustls::pki_types::UnixTime,
    ) -> Result<rustls::client::danger::ServerCertVerified, rustls::Error> {
        Ok(rustls::client::danger::ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &rustls::pki_types::CertificateDer<'_>,
        _dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        Ok(rustls::client::danger::HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &rustls::pki_types::CertificateDer<'_>,
        _dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        Ok(rustls::client::danger::HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<rustls::SignatureScheme> {
        rustls::crypto::ring::default_provider()
            .signature_verification_algorithms
            .supported_schemes()
    }
}

/// Runtime representation of a cluster — owns all per-cluster state.
///
/// This is the "live" counterpart of `ClusterConfig`. While `ClusterConfig` is
/// a pure serde struct describing *what* a cluster should look like, `Cluster`
/// holds the mutable runtime state: load balancer, circuit breakers, health
/// status, and discovered nodes.
#[derive(Clone)]
pub struct Cluster {
    /// Immutable snapshot of the cluster's declarative config.
    config: Arc<ClusterConfig>,

    /// Pre-interned `Arc<str>` copies of hot config fields so that
    /// `select_upstream()` only bumps a reference count instead of
    /// heap-allocating a fresh `String` on every request.
    scheme: Arc<str>,
    pass_host: Arc<str>,
    upstream_host: Option<Arc<str>>,

    /// Per-cluster HTTP client with connection pool configured from
    /// `ClusterConfig::keepalive_pool`. Each cluster owns its own pool
    /// so that different clusters can have different idle_timeout, pool_size, etc.
    /// Wraps an `HttpsConnector` so that both `http://` and `https://` upstreams
    /// are supported (TLS via rustls). HTTP/2 is negotiated automatically via ALPN.
    http_client: Client<HttpsConnector<HttpConnector>, BoxBody>,

    /// Load balancer (round-robin / random / least-request / peak-ewma).
    lb: Arc<LoadBalancer>,

    /// Per-node circuit breakers.
    circuit_breakers: Arc<CircuitBreakerRegistry>,

    /// Per-node health status from active health checks.
    node_health: Arc<DashMap<String, bool>>,
    health_check_count: Arc<DashMap<String, AtomicU32>>,

    /// Consul-discovered nodes (if service discovery is enabled).
    discovered_nodes: Arc<arc_swap::ArcSwap<Vec<UpstreamNode>>>,
}

impl Cluster {
    pub fn new(config: ClusterConfig) -> Self {
        let lb = LoadBalancer::new(&config.lb_type);
        if !config.nodes.is_empty() {
            lb.update_instances(&config.nodes);
        }

        let http_client = build_cluster_http_client(
            &config.keepalive_pool,
            config.tls_verify,
            config.timeout.connect,
        );
        let scheme: Arc<str> = Arc::from(config.scheme.as_str());
        let pass_host: Arc<str> = Arc::from(config.pass_host.as_str());
        let upstream_host: Option<Arc<str>> = config.upstream_host.as_deref().map(Arc::from);

        Self {
            config: Arc::new(config),
            scheme,
            pass_host,
            upstream_host,
            http_client,
            lb,
            circuit_breakers: Arc::new(CircuitBreakerRegistry::new()),
            node_health: Arc::new(DashMap::new()),
            health_check_count: Arc::new(DashMap::new()),
            discovered_nodes: Arc::new(arc_swap::ArcSwap::new(Arc::new(Vec::new()))),
        }
    }

    // ---- Config accessors ----

    pub fn name(&self) -> &str {
        &self.config.name
    }

    pub fn config(&self) -> &ClusterConfig {
        &self.config
    }

    pub fn http_client(&self) -> &Client<HttpsConnector<HttpConnector>, BoxBody> {
        &self.http_client
    }

    pub fn lb(&self) -> &Arc<LoadBalancer> {
        &self.lb
    }

    // ---- Node selection ----

    pub fn select_upstream(&self) -> Option<(UpstreamTarget, RequestGuard)> {
        let guard = self.lb.select()?;

        let target = UpstreamTarget {
            instance: guard.instance.clone(),
            scheme: self.scheme.clone(),
            pass_host: self.pass_host.clone(),
            upstream_host: self.upstream_host.clone(),
        };

        Some((target, guard))
    }

    // ---- Health state ----

    pub fn is_node_healthy(&self, node_key: &str) -> bool {
        self.node_health
            .get(node_key)
            .map(|v| *v.value())
            .unwrap_or(true)
    }

    pub fn mark_node_healthy(&self, node_key: &str) {
        self.node_health.insert(node_key.to_string(), true);
        self.reset_health_count(node_key);
    }

    pub fn mark_node_unhealthy(&self, node_key: &str) {
        self.node_health.insert(node_key.to_string(), false);
        self.reset_health_count(node_key);
    }

    /// Increment consecutive health check counter (success or failure streak).
    /// Returns the new count. The caller decides the semantics (success vs failure).
    pub fn record_health_check(&self, node_key: &str) -> u32 {
        if let Some(entry) = self.health_check_count.get(node_key) {
            return entry.value().fetch_add(1, Ordering::Relaxed) + 1;
        }
        let counter = self
            .health_check_count
            .entry(node_key.to_string())
            .or_insert_with(|| AtomicU32::new(0));
        counter.value().fetch_add(1, Ordering::Relaxed) + 1
    }

    /// Reset the consecutive health check counter for a node.
    pub fn reset_health_count(&self, node_key: &str) {
        if let Some(entry) = self.health_check_count.get(node_key) {
            entry.value().store(0, Ordering::Relaxed);
        }
    }

    // ---- Circuit breaker ----

    pub fn circuit_breakers(&self) -> &CircuitBreakerRegistry {
        &self.circuit_breakers
    }

    // ---- Service discovery ----

    /// Update discovered nodes (from Consul) and push them into the LB.
    /// Purges stale entries from health/breaker maps for nodes no longer present.
    pub fn update_discovered_nodes(&self, nodes: Vec<UpstreamNode>) {
        self.lb.update_instances(&nodes);
        self.discovered_nodes.store(Arc::new(nodes));
        self.purge_stale_nodes();
    }

    pub fn discovered_nodes(&self) -> Arc<Vec<UpstreamNode>> {
        self.discovered_nodes.load_full()
    }

    /// Return discovered nodes if non-empty, otherwise static nodes.
    pub fn effective_nodes(&self) -> Vec<UpstreamNode> {
        let discovered = self.discovered_nodes.load();
        if !discovered.is_empty() {
            discovered.to_vec()
        } else {
            self.config.nodes.clone()
        }
    }

    /// Total node count (discovered + static fallback).
    pub fn node_count(&self) -> usize {
        let discovered = self.discovered_nodes.load();
        if !discovered.is_empty() {
            discovered.len()
        } else {
            self.config.nodes.len()
        }
    }

    // ---- Stale node cleanup ----

    /// Remove health status, counters, and circuit breaker entries for nodes
    /// that are no longer in the effective node set. This prevents unbounded
    /// growth of DashMaps when nodes are dynamically added/removed (e.g. via
    /// service discovery or config hot-reload).
    pub fn purge_stale_nodes(&self) {
        let active_keys: HashSet<String> = self
            .effective_nodes()
            .iter()
            .map(|n| format!("{}:{}", n.host, n.port))
            .collect();

        self.node_health.retain(|k, _| active_keys.contains(k));
        self.health_check_count
            .retain(|k, _| active_keys.contains(k));
        self.circuit_breakers.retain_nodes(&active_keys);
    }

    // ---- Config update ----

    /// Update the cluster's config. Preserves runtime state (LB counters,
    /// circuit breaker state, health state). Only updates the config snapshot
    /// and refreshes static nodes in the LB if they changed.
    pub fn update_config(&self, new_config: ClusterConfig) -> Self {
        let new_lb = if new_config.lb_type != self.config.lb_type {
            // LB type changed — must create a new balancer.
            let lb = LoadBalancer::new(&new_config.lb_type);
            if !new_config.nodes.is_empty() {
                lb.update_instances(&new_config.nodes);
            }
            lb
        } else {
            // Same LB type — reuse existing (preserves counters).
            if !new_config.nodes.is_empty() {
                self.lb.update_instances(&new_config.nodes);
            }
            self.lb.clone()
        };

        // Rebuild HTTP client if pool config, TLS, or connect timeout changed.
        let new_client = if new_config.keepalive_pool != self.config.keepalive_pool
            || new_config.tls_verify != self.config.tls_verify
            || new_config.timeout.connect != self.config.timeout.connect
        {
            build_cluster_http_client(
                &new_config.keepalive_pool,
                new_config.tls_verify,
                new_config.timeout.connect,
            )
        } else {
            self.http_client.clone()
        };

        let scheme: Arc<str> = Arc::from(new_config.scheme.as_str());
        let pass_host: Arc<str> = Arc::from(new_config.pass_host.as_str());
        let upstream_host: Option<Arc<str>> = new_config.upstream_host.as_deref().map(Arc::from);

        Self {
            config: Arc::new(new_config),
            scheme,
            pass_host,
            upstream_host,
            http_client: new_client,
            lb: new_lb,
            circuit_breakers: self.circuit_breakers.clone(),
            node_health: self.node_health.clone(),
            health_check_count: self.health_check_count.clone(),
            discovered_nodes: self.discovered_nodes.clone(),
        }
    }
}

/// Central registry of all live clusters. Thread-safe, cheaply cloneable.
#[derive(Clone)]
pub struct ClusterStore {
    clusters: Arc<DashMap<String, Cluster>>,
}

impl Default for ClusterStore {
    fn default() -> Self {
        Self {
            clusters: Arc::new(DashMap::new()),
        }
    }
}

impl ClusterStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Get a cluster by name.
    pub fn get(&self, name: &str) -> Option<Cluster> {
        self.clusters.get(name).map(|entry| entry.value().clone())
    }

    /// Upsert a cluster from config. If the cluster already exists, update its
    /// config while preserving runtime state. If new, create fresh.
    /// Purges stale node entries when nodes change.
    pub fn upsert(&self, config: ClusterConfig) {
        let name = config.name.clone();
        if let Some(existing) = self.clusters.get(&name) {
            let updated = existing.value().update_config(config);
            drop(existing);
            updated.purge_stale_nodes();
            self.clusters.insert(name, updated);
        } else {
            self.clusters.insert(name, Cluster::new(config));
        }
    }

    /// Remove a cluster.
    pub fn remove(&self, name: &str) -> bool {
        self.clusters.remove(name).is_some()
    }

    /// Iterate over all clusters. The callback receives (name, cluster).
    pub fn for_each(&self, mut f: impl FnMut(&str, &Cluster)) {
        for entry in self.clusters.iter() {
            f(entry.key(), entry.value());
        }
    }

    /// Initialize from a list of cluster configs.
    pub fn init_from_configs(&self, clusters: &[ClusterConfig]) {
        for config in clusters {
            self.upsert(config.clone());
        }
    }
}

/// Build a hyper `Client` that supports both HTTP and HTTPS upstreams.
///
/// - Plain `http://` connections go through the inner `HttpConnector` directly.
/// - `https://` connections are terminated with rustls (ring backend).
/// - HTTP/2 is negotiated automatically via ALPN for TLS connections;
///   plain HTTP connections stay on HTTP/1.1.
/// - When `tls_verify` is `false` (the default), certificate validation is
///   skipped — suitable for internal / mesh traffic with self-signed certs.
fn build_cluster_http_client(
    pool_cfg: &KeepalivePoolConfig,
    tls_verify: bool,
    connect_timeout_secs: f64,
) -> Client<HttpsConnector<HttpConnector>, BoxBody> {
    let mut http = HttpConnector::new();
    http.set_nodelay(true);
    http.set_keepalive(Some(Duration::from_secs(pool_cfg.idle_timeout)));
    http.set_connect_timeout(Some(Duration::from_secs_f64(connect_timeout_secs)));
    http.enforce_http(false);

    let https = if tls_verify {
        hyper_rustls::HttpsConnectorBuilder::new()
            .with_webpki_roots()
            .https_or_http()
            .enable_http1()
            .enable_http2()
            .wrap_connector(http)
    } else {
        let tls_config = rustls::ClientConfig::builder()
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(NoVerifier))
            .with_no_client_auth();

        hyper_rustls::HttpsConnectorBuilder::new()
            .with_tls_config(tls_config)
            .https_or_http()
            .enable_http1()
            .enable_http2()
            .wrap_connector(http)
    };

    Client::builder(TokioExecutor::new())
        .pool_idle_timeout(Duration::from_secs(pool_cfg.idle_timeout))
        .pool_max_idle_per_host(pool_cfg.size)
        .build(https)
}
