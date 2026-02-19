use crate::config::{DiscoveryArgs, GatewayConfig, UpstreamNode};
use crate::{config, discovery, server, upstream};
use anyhow::Result;
use arc_swap::ArcSwap;
use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::Notify;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

/// CLI arguments forwarded from `main()`.
pub struct BootstrapArgs {
    pub config_path: std::path::PathBuf,
    pub listen: String,
    pub admin_listen: String,
}

/// Gateway lifecycle: init → resolve → watch → serve → shutdown.
pub async fn run(args: BootstrapArgs) -> Result<()> {
    init_tracing();

    // Phase 1: build state (connects to etcd but does not register yet).
    let gateway = config::GatewayConfig::load(&args.config_path)?;
    let state = server::GatewayState::new(gateway).await?;

    // Phase 2: synchronous initial resolve — upstream nodes must be ready before traffic.
    poll_consul_services(&state.config, &state.upstream).await?;
    tracing::info!("discovery: consul: initial resolve completed");

    // Phase 3: start continuous watchers — all loops owned here.
    let shutdown = Arc::new(Notify::new());
    start_config_watcher(&state, &shutdown);
    start_discovery_loop(&state, &shutdown);
    start_health_check_loop(&state.upstream, &shutdown);

    // Phase 4: register in etcd + start keepalive/watch (quota splitting starts here).
    start_instance_registry(&state.infra, &shutdown).await?;

    // Phase 5: self-registration + admin/proxy servers.
    let consul_registry = setup_consul_registry(&state, &args).await;
    if let Some(ref reg) = consul_registry {
        start_consul_heartbeat(reg.clone(), &shutdown);
    }
    start_admin_server(&state, &args);

    tracing::info!("server: starting gateway, listen={}", args.listen);

    let proxy_handle = tokio::spawn({
        let listen = args.listen.clone();
        let state = state.clone();
        let shutdown = shutdown.clone();
        async move { server::run_proxy_server(&listen, state, shutdown).await }
    });

    // Phase 6: block until signal, then clean up.
    wait_for_shutdown(&shutdown).await;

    // Graceful shutdown.
    state.infra.shutdown().await;
    if let Some(ref reg) = consul_registry {
        if let Err(e) = reg.deregister().await {
            tracing::error!("consul: deregister on shutdown failed: {}", e);
        }
    }

    // Wait for proxy to finish draining.
    if let Err(e) = proxy_handle.await {
        tracing::error!("server: proxy task error: {}", e);
    }

    tracing::info!("server: shutdown complete");
    Ok(())
}

fn init_tracing() {
    let (non_blocking, _guard) = tracing_appender::non_blocking::NonBlockingBuilder::default()
        .buffered_lines_limit(128_000)
        .lossy(true)
        .finish(std::io::stdout());

    tracing_subscriber::registry()
        .with(EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")))
        .with(
            tracing_subscriber::fmt::layer()
                .with_writer(non_blocking)
                .with_ansi(false)
                .with_target(false)
                .json(),
        )
        .init();

    std::mem::forget(_guard);
}

// ---------------------------------------------------------------------------
// Consul service discovery — orchestration logic that reads config, calls the
// pure ConsulClient API, and writes results back to ClusterStore + Metrics.
// ---------------------------------------------------------------------------

struct ClusterDiscovery {
    cluster_name: String,
    service_name: String,
    discovery_args: Option<DiscoveryArgs>,
}

/// Single round of consul service discovery.
async fn poll_consul_services(
    config: &Arc<ArcSwap<GatewayConfig>>,
    cluster_store: &upstream::ClusterStore,
) -> anyhow::Result<()> {
    let client = {
        let cfg = config.load();
        discovery::ConsulClient::new(
            &cfg.consul.address,
            cfg.consul.token.clone(),
            cfg.consul.datacenter.clone(),
        )
    };

    let cluster_descriptors: Vec<ClusterDiscovery> = {
        let mut descriptors = Vec::new();
        cluster_store.for_each(|_name, cluster| {
            let cfg = cluster.config();
            if cfg.discovery_type.as_deref() == Some("consul") {
                if let Some(ref s) = cfg.service_name {
                    descriptors.push(ClusterDiscovery {
                        cluster_name: cfg.name.clone(),
                        service_name: s.clone(),
                        discovery_args: cfg.discovery_args.clone(),
                    });
                }
            }
        });
        descriptors
    };

    let unique_services: Vec<&str> = {
        let mut seen = HashSet::new();
        cluster_descriptors
            .iter()
            .filter(|cd| seen.insert(cd.service_name.as_str()))
            .map(|cd| cd.service_name.as_str())
            .collect()
    };

    let mut raw_nodes: HashMap<&str, Vec<discovery::ConsulServiceNode>> = HashMap::new();

    for service_name in &unique_services {
        let start = Instant::now();

        let consul_nodes = match client.query_healthy_services(service_name).await {
            Ok(nodes) => nodes,
            Err(e) => {
                tracing::warn!(
                    "discovery: consul: query failed, service={}, error={}",
                    service_name,
                    e
                );
                metrics::counter!(
                    "gateway_consul_poll_total",
                    "service_name" => service_name.to_string(),
                    "result" => "error",
                )
                .increment(1);
                continue;
            }
        };

        let duration = start.elapsed().as_secs_f64();
        tracing::info!(
            "discovery: consul: queried, service={}, raw_nodes={}, duration={:.3}s",
            service_name,
            consul_nodes.len(),
            duration,
        );

        metrics::counter!(
            "gateway_consul_poll_total",
            "service_name" => service_name.to_string(),
            "result" => "success",
        )
        .increment(1);

        raw_nodes.insert(service_name, consul_nodes);
    }

    for cd in &cluster_descriptors {
        let Some(consul_nodes) = raw_nodes.get(cd.service_name.as_str()) else {
            continue;
        };

        let nodes: Vec<UpstreamNode> = consul_nodes
            .iter()
            .filter(|n| match &cd.discovery_args {
                Some(args) => metadata_matches(&n.service_meta, &args.metadata_match),
                None => true,
            })
            .map(to_upstream_node)
            .collect();

        tracing::info!(
            "discovery: consul: cluster updated, cluster={}, service={}, nodes={}",
            cd.cluster_name,
            cd.service_name,
            nodes.len(),
        );

        metrics::gauge!(
            "gateway_consul_discovered_nodes",
            "cluster" => cd.cluster_name.clone(),
        )
        .set(nodes.len() as f64);

        if let Some(cluster) = cluster_store.get(&cd.cluster_name) {
            cluster.update_discovered_nodes(nodes);
        }
    }

    Ok(())
}

fn to_upstream_node(n: &discovery::ConsulServiceNode) -> UpstreamNode {
    let weight = n
        .service_meta
        .get("weight")
        .and_then(|w| w.parse::<u32>().ok())
        .unwrap_or(1);

    UpstreamNode {
        host: n.service_address.clone(),
        port: n.service_port,
        weight,
        metadata: n.service_meta.clone(),
    }
}

fn metadata_matches(
    meta: &HashMap<String, String>,
    filters: &HashMap<String, Vec<String>>,
) -> bool {
    for (key, allowed_values) in filters {
        if let Some(meta_value) = meta.get(key) {
            if !allowed_values.contains(meta_value) {
                return false;
            }
        } else {
            return false;
        }
    }
    true
}

// ---------------------------------------------------------------------------
// Loop owners — each function spawns a task with the retry/interval loop.
// The etcd/consul/upstream modules only provide single-shot operations.
// ---------------------------------------------------------------------------

/// Sleep for `duration`, but return `true` immediately if shutdown is signalled.
/// Returns `false` if the full duration elapsed normally.
async fn sleep_or_shutdown(duration: std::time::Duration, shutdown: &Notify) -> bool {
    tokio::select! {
        _ = tokio::time::sleep(duration) => false,
        _ = shutdown.notified() => true,
    }
}

fn start_config_watcher(state: &server::GatewayState, shutdown: &Arc<Notify>) {
    let Some(etcd) = state.infra.etcd_client().cloned() else {
        tracing::info!("etcd: config watcher skipped, no endpoints configured");
        return;
    };

    let etcd_cfg = state.config.load().etcd.clone();
    let prefixes = config::etcd::compute_prefixes(&etcd_cfg);

    let state = state.clone();
    let shutdown = shutdown.clone();

    tokio::spawn(async move {
        // --- Initial load ---
        let initial = match config::etcd::initial_load(&etcd, &prefixes).await {
            Ok(data) => data,
            Err(e) => {
                tracing::error!("etcd: initial load failed, error={}", e);
                return;
            }
        };

        // Apply initial data to state.
        for cluster in initial.clusters {
            state.upsert_cluster(cluster).await;
        }
        for domain in initial.domains {
            state.upsert_domain(domain).await;
        }
        if let Some(reg) = state.infra.instance_registry() {
            reg.set_config_revision(if initial.meta_revision > 0 {
                initial.meta_revision
            } else {
                0
            });
        }

        let mut revision = initial.revision;

        // --- Watch loop with reconnect ---
        loop {
            tracing::info!("etcd: watch starting, revision={}", revision);

            let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();

            // Spawn the watch_once in a separate task so we can select on shutdown.
            let etcd_c = etcd.clone();
            let prefixes_c = config::etcd::EtcdPrefixes {
                domain_prefix: prefixes.domain_prefix.clone(),
                cluster_prefix: prefixes.cluster_prefix.clone(),
                meta_revision_key: prefixes.meta_revision_key.clone(),
            };
            let watch_handle = tokio::spawn(async move {
                config::etcd::watch_once(&etcd_c, &prefixes_c, revision, tx).await
            });

            // Process events until watch ends or shutdown.
            loop {
                tokio::select! {
                    event = rx.recv() => {
                        match event {
                            Some(config::etcd::ConfigEvent::DomainUpsert(domain)) => {
                                state.upsert_domain(domain).await;
                                metrics::counter!(
                                    "gateway_config_reloads_total",
                                    "source" => "etcd", "result" => "success",
                                ).increment(1);
                            }
                            Some(config::etcd::ConfigEvent::DomainDelete(name)) => {
                                state.delete_domain(&name).await;
                                metrics::counter!(
                                    "gateway_config_reloads_total",
                                    "source" => "etcd", "result" => "success",
                                ).increment(1);
                            }
                            Some(config::etcd::ConfigEvent::ClusterUpsert(cluster)) => {
                                state.upsert_cluster(*cluster).await;
                                metrics::counter!(
                                    "gateway_config_reloads_total",
                                    "source" => "etcd", "result" => "success",
                                ).increment(1);
                            }
                            Some(config::etcd::ConfigEvent::ClusterDelete(name)) => {
                                state.delete_cluster(&name).await;
                                metrics::counter!(
                                    "gateway_config_reloads_total",
                                    "source" => "etcd", "result" => "success",
                                ).increment(1);
                            }
                            Some(config::etcd::ConfigEvent::MetaRevision(rev)) => {
                                if let Some(reg) = state.infra.instance_registry() {
                                    reg.set_config_revision(rev);
                                }
                            }
                            Some(config::etcd::ConfigEvent::ParseError { .. }) => {
                                metrics::counter!(
                                    "gateway_config_reloads_total",
                                    "source" => "etcd", "result" => "error",
                                ).increment(1);
                            }
                            None => break, // channel closed, watch ended
                        }
                    }
                    _ = shutdown.notified() => {
                        watch_handle.abort();
                        return;
                    }
                }
            }

            // Watch ended — get final revision.
            match watch_handle.await {
                Ok(Ok(new_rev)) => {
                    revision = new_rev;
                    tracing::warn!("etcd: watch stream ended, reconnecting...");
                }
                Ok(Err(e)) => {
                    tracing::error!("etcd: watch error, retrying in 5s, error={}", e);
                }
                Err(e) => {
                    tracing::error!("etcd: watch task panicked: {}", e);
                }
            }

            // Backoff before reconnect.
            if sleep_or_shutdown(std::time::Duration::from_secs(5), &shutdown).await {
                return;
            }
        }
    });
}

fn start_discovery_loop(state: &server::GatewayState, shutdown: &Arc<Notify>) {
    let config = state.config.clone();
    let cluster_store = state.upstream.clone();
    let wake = state.infra.discovery_wake();
    let shutdown = shutdown.clone();

    tokio::spawn(async move {
        loop {
            let poll_interval = config.load().consul.poll_interval_secs;

            match poll_consul_services(&config, &cluster_store).await {
                Ok(_) => {
                    tracing::debug!("discovery: consul: poll completed");
                }
                Err(e) => {
                    tracing::error!("discovery: consul: poll failed, error={}", e);
                }
            }

            tokio::select! {
                _ = tokio::time::sleep(tokio::time::Duration::from_secs(poll_interval)) => {}
                _ = wake.notified() => {
                    tracing::info!("discovery: consul: immediate poll triggered by config reload");
                }
                _ = shutdown.notified() => return,
            }
        }
    });
}

fn start_health_check_loop(cluster_store: &upstream::ClusterStore, shutdown: &Arc<Notify>) {
    let store = cluster_store.clone();
    let shutdown = shutdown.clone();

    tokio::spawn(async move {
        let client = upstream::build_health_check_client();
        loop {
            if sleep_or_shutdown(std::time::Duration::from_secs(10), &shutdown).await {
                return;
            }
            upstream::run_health_checks(&store, &client).await;
        }
    });
}

async fn start_instance_registry(infra: &server::InfraState, shutdown: &Arc<Notify>) -> Result<()> {
    let Some(registry) = infra.instance_registry() else {
        return Ok(());
    };

    let count = registry.register().await?;
    tracing::info!("instance_registry: registered, peers={}", count);

    // Keepalive loop.
    {
        let registry = registry.clone();
        let shutdown = shutdown.clone();
        let interval = registry.keepalive_interval();

        tokio::spawn(async move {
            loop {
                if sleep_or_shutdown(interval, &shutdown).await {
                    return;
                }
                if let Err(e) = registry.keepalive_once().await {
                    tracing::error!("instance_registry: keepalive cycle failed: {}", e);
                }
            }
        });
    }

    // Watch loop.
    {
        let registry = registry.clone();
        let shutdown = shutdown.clone();

        tokio::spawn(async move {
            loop {
                tokio::select! {
                    _ = registry.watch_instances_once() => {
                        tracing::warn!("instance_registry: watch stream ended, reconnecting...");
                    }
                    _ = shutdown.notified() => return,
                }
                tokio::time::sleep(std::time::Duration::from_secs(3)).await;
            }
        });
    }

    Ok(())
}

fn start_consul_heartbeat(registry: Arc<discovery::ConsulRegistry>, shutdown: &Arc<Notify>) {
    let shutdown = shutdown.clone();
    let interval = registry.heartbeat_interval();

    tokio::spawn(async move {
        // Initial TTL pass.
        if let Err(e) = registry.pass_ttl().await {
            tracing::error!("consul: initial TTL pass failed: {}", e);
        }

        loop {
            if sleep_or_shutdown(interval, &shutdown).await {
                tracing::info!("consul: heartbeat shutdown signal received");
                return;
            }

            match registry.pass_ttl().await {
                Ok(_) => {
                    tracing::debug!("consul: TTL heartbeat sent");
                }
                Err(e) => {
                    tracing::error!("consul: TTL heartbeat failed: {}", e);
                    // Re-register in case the service was removed.
                    if let Err(re) = registry.register().await {
                        tracing::error!("consul: re-register failed: {}", re);
                    }
                }
            }
        }
    });
}

fn start_admin_server(state: &server::GatewayState, args: &BootstrapArgs) {
    let s = state.clone();
    let admin_addr = args.admin_listen.clone();
    tokio::spawn(async move {
        if let Err(e) = server::run_admin_server(&admin_addr, s).await {
            tracing::error!("server: admin failed, error={}", e);
        }
    });
}

async fn setup_consul_registry(
    state: &server::GatewayState,
    args: &BootstrapArgs,
) -> Option<Arc<discovery::ConsulRegistry>> {
    let cfg = state.config.load();
    if !cfg.registration.enabled {
        tracing::info!("consul: self-registration disabled");
        return None;
    }

    let client = discovery::ConsulClient::new(
        &cfg.consul.address,
        cfg.consul.token.clone(),
        cfg.consul.datacenter.clone(),
    );

    match discovery::ConsulRegistry::new(client, &args.listen, cfg.registration.clone()) {
        Ok(r) => {
            let r = Arc::new(r);
            if let Err(e) = r.register().await {
                tracing::error!("consul: initial registration failed: {}", e);
            }
            Some(r)
        }
        Err(e) => {
            tracing::error!("consul: failed to create registry: {}", e);
            None
        }
    }
}

async fn wait_for_shutdown(shutdown: &Arc<Notify>) {
    let ctrl_c = tokio::signal::ctrl_c();

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => tracing::info!("server: received SIGINT, shutting down"),
        _ = terminate => tracing::info!("server: received SIGTERM, shutting down"),
    }

    // Signal all background loops to stop.
    shutdown.notify_waiters();
}
