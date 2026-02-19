use crate::config::{ClusterConfig, DomainConfig, GatewayConfig};
use crate::etcd::EtcdClient;
use crate::metrics::Metrics;
use crate::routing::RouteTable;
use crate::server::instance_registry::InstanceRegistry;
use crate::upstream::ClusterStore;
use anyhow::Result;
use arc_swap::ArcSwap;
use std::sync::atomic::AtomicU32;
use std::sync::Arc;
use tokio::sync::{Mutex, Notify};
use tracing::info;

// ---------------------------------------------------------------------------
// Sub-states — each represents a cohesive domain boundary.
// Consumers should depend on the narrowest sub-state they need.
// ---------------------------------------------------------------------------

/// Routing domain: route table and instance count for distributed rate limiting.
#[derive(Clone)]
pub struct RoutingState {
    pub route_table: Arc<ArcSwap<RouteTable>>,
    instance_count: Option<Arc<AtomicU32>>,
    /// Snapshot of domains currently loaded (from etcd).
    domains: Arc<ArcSwap<Vec<DomainConfig>>>,
}

impl RoutingState {
    fn rebuild_table(&self, domains: &[DomainConfig]) {
        let new_table = RouteTable::new(domains, self.instance_count.clone());
        self.route_table.store(Arc::new(new_table));
        self.domains.store(Arc::new(domains.to_vec()));
    }

    pub fn domain_count(&self) -> usize {
        self.domains.load().len()
    }

    pub fn route_count(&self) -> usize {
        self.domains.load().iter().map(|d| d.routes.len()).sum()
    }

    pub fn domains(&self) -> arc_swap::Guard<Arc<Vec<DomainConfig>>> {
        self.domains.load()
    }
}

/// Infrastructure: etcd client, instance registry, discovery wake.
#[derive(Clone)]
pub struct InfraState {
    etcd_client: Option<EtcdClient>,
    instance_registry: Option<Arc<InstanceRegistry>>,
    discovery_wake: Arc<Notify>,
}

impl InfraState {
    pub fn etcd_client(&self) -> Option<&EtcdClient> {
        self.etcd_client.as_ref()
    }

    pub fn instance_registry(&self) -> Option<&Arc<InstanceRegistry>> {
        self.instance_registry.as_ref()
    }

    pub fn discovery_wake(&self) -> Arc<Notify> {
        self.discovery_wake.clone()
    }

    pub fn trigger_discovery(&self) {
        self.discovery_wake.notify_one();
    }

    pub async fn shutdown(&self) {
        if let Some(ref registry) = self.instance_registry {
            registry.shutdown().await;
        }
    }
}

// ---------------------------------------------------------------------------
// GatewayState — root aggregate composed of sub-states.
// ---------------------------------------------------------------------------

/// Shared gateway state, cheaply cloneable.
///
/// Composed of domain-specific sub-states. Pass the narrowest sub-state
/// to each subsystem to avoid leaking unrelated dependencies.
///
/// All config mutations are serialized through `config_mu` to prevent
/// read-modify-write races. Reads via `ArcSwap::load` remain lock-free.
#[derive(Clone)]
pub struct GatewayState {
    pub config: Arc<ArcSwap<GatewayConfig>>,
    pub metrics: Metrics,
    pub routing: RoutingState,
    pub upstream: ClusterStore,
    pub infra: InfraState,
    /// Serializes all config mutations (upsert/delete/reload) to prevent
    /// concurrent read-modify-write from losing updates.
    config_mu: Arc<Mutex<()>>,
}

impl GatewayState {
    pub async fn new(config: GatewayConfig) -> Result<Self> {
        let etcd_client = if !config.etcd.endpoints.is_empty() {
            let client = EtcdClient::connect(&config.etcd).await?;
            info!("etcd: connected to {}", client.base_url());
            Some(client)
        } else {
            None
        };

        let instance_count = if config.instance_registry.enabled {
            Some(Arc::new(AtomicU32::new(1)))
        } else {
            None
        };

        let instance_registry = if config.instance_registry.enabled {
            let etcd = etcd_client
                .clone()
                .ok_or_else(|| anyhow::anyhow!("instance_registry requires etcd endpoints"))?;
            let ic = instance_count
                .clone()
                .expect("instance_count must be Some when instance_registry is enabled");
            let registry = InstanceRegistry::new(etcd, &config.instance_registry, ic);
            info!("instance_registry: prepared, id={}", registry.instance_id(),);
            Some(Arc::new(registry))
        } else {
            info!("instance_registry: disabled (standalone rate limiting)");
            None
        };

        let cluster_store = ClusterStore::new();
        // No local domains/clusters — all business config comes from etcd.

        let empty_domains: Vec<DomainConfig> = Vec::new();
        let route_table = RouteTable::new(&empty_domains, instance_count.clone());
        let metrics = Metrics::install();
        metrics::gauge!("gateway_config_routes_total").set(0.0);

        Ok(Self {
            config: Arc::new(ArcSwap::new(Arc::new(config))),
            metrics,
            routing: RoutingState {
                route_table: Arc::new(ArcSwap::new(Arc::new(route_table))),
                instance_count,
                domains: Arc::new(ArcSwap::new(Arc::new(Vec::new()))),
            },
            upstream: cluster_store,
            infra: InfraState {
                etcd_client,
                instance_registry,
                discovery_wake: Arc::new(Notify::new()),
            },
            config_mu: Arc::new(Mutex::new(())),
        })
    }

    /// Incrementally upsert a single domain (from etcd).
    pub async fn upsert_domain(&self, domain: DomainConfig) {
        let _guard = self.config_mu.lock().await;
        let mut domains = (**self.routing.domains.load()).clone();

        match domains.iter_mut().find(|d| d.name == domain.name) {
            Some(existing) => *existing = domain.clone(),
            None => domains.push(domain.clone()),
        }

        self.routing.rebuild_table(&domains);
        self.update_route_metric();
        self.infra.trigger_discovery();
        info!("config: domain upserted, name={}", domain.name);
    }

    /// Incrementally delete a single domain (from etcd).
    pub async fn delete_domain(&self, domain_name: &str) {
        let _guard = self.config_mu.lock().await;
        let mut domains = (**self.routing.domains.load()).clone();
        let before = domains.len();
        domains.retain(|d| d.name != domain_name);

        if domains.len() == before {
            info!(
                "config: domain delete ignored (not found), name={}",
                domain_name
            );
            return;
        }

        self.routing.rebuild_table(&domains);
        self.update_route_metric();
        self.infra.trigger_discovery();
        info!("config: domain deleted, name={}", domain_name);
    }

    /// Incrementally upsert a single cluster (from etcd).
    pub async fn upsert_cluster(&self, cluster: ClusterConfig) {
        let _guard = self.config_mu.lock().await;
        self.upstream.upsert(cluster.clone());
        self.infra.trigger_discovery();
        info!("config: cluster upserted, name={}", cluster.name);
    }

    /// Incrementally delete a single cluster (from etcd).
    pub async fn delete_cluster(&self, cluster_name: &str) {
        let _guard = self.config_mu.lock().await;
        if !self.upstream.remove(cluster_name) {
            info!(
                "config: cluster delete ignored (not found), name={}",
                cluster_name
            );
            return;
        }
        self.infra.trigger_discovery();
        info!("config: cluster deleted, name={}", cluster_name);
    }

    // -- private helpers --

    fn update_route_metric(&self) {
        metrics::gauge!("gateway_config_routes_total").set(self.routing.route_count() as f64);
    }
}
