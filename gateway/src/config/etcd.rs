use crate::config::{ClusterConfig, DomainConfig, EtcdConfig};
use crate::etcd::{
    EtcdClient,
    client::{
        b64_decode, b64_encode, prefix_range_end, RangeRequest, WatchCreate, WatchCreateRequest,
    },
};
use anyhow::Result;
use tracing::{error, info, warn};

// ---------------------------------------------------------------------------
// Pure data types — no dependency on server::GatewayState.
// ---------------------------------------------------------------------------

/// A parsed configuration event from an etcd watch stream.
pub enum ConfigEvent {
    DomainUpsert(DomainConfig),
    DomainDelete(String),
    ClusterUpsert(ClusterConfig),
    ClusterDelete(String),
    MetaRevision(i64),
    /// A parse error was encountered (non-fatal, caller may count as metric).
    ParseError { prefix_kind: &'static str, key: String, error: String },
}

/// Result of initial config load from etcd.
pub struct InitialLoad {
    pub domains: Vec<DomainConfig>,
    pub clusters: Vec<ClusterConfig>,
    pub revision: i64,
    pub meta_revision: i64,
}

// ---------------------------------------------------------------------------
// Public API — stateless functions that only need EtcdClient + config.
// ---------------------------------------------------------------------------

/// Compute the normalized prefixes from config.
pub struct EtcdPrefixes {
    pub domain_prefix: String,
    pub cluster_prefix: String,
    pub meta_revision_key: String,
}

pub fn compute_prefixes(etcd_cfg: &EtcdConfig) -> EtcdPrefixes {
    let dp = normalize_prefix(&etcd_cfg.domain_prefix);
    let cp = normalize_prefix(&etcd_cfg.cluster_prefix);
    let meta_key = format!(
        "{}/config_revision",
        etcd_cfg
            .meta_prefix
            .as_deref()
            .unwrap_or("/hermes/meta")
            .trim_end_matches('/')
    );
    EtcdPrefixes {
        domain_prefix: dp,
        cluster_prefix: cp,
        meta_revision_key: meta_key,
    }
}

/// Load all domains and clusters from etcd (range scan). Returns parsed data
/// without touching any shared state — the caller is responsible for applying.
pub async fn initial_load(client: &EtcdClient, prefixes: &EtcdPrefixes) -> Result<InitialLoad> {
    let (clusters, cluster_rev) = load_prefix::<ClusterConfig>(client, &prefixes.cluster_prefix, "cluster").await?;
    let (domains, domain_rev) = load_prefix::<DomainConfig>(client, &prefixes.domain_prefix, "domain").await?;

    let revision = cluster_rev.max(domain_rev);
    let meta_revision = read_meta_revision(client, &prefixes.meta_revision_key).await;

    info!(
        "etcd: initial load, domains={}, clusters={}, revision={}, meta_revision={}",
        domains.len(), clusters.len(), revision, meta_revision
    );

    Ok(InitialLoad { domains, clusters, revision, meta_revision })
}

/// Open three concurrent watch streams (domain, cluster, meta) and yield
/// `ConfigEvent`s until any stream ends or errors. Returns the latest
/// etcd revision observed, so the caller can reconnect from there.
///
/// This function does NOT loop or reconnect — the caller owns the retry loop.
pub async fn watch_once(
    client: &EtcdClient,
    prefixes: &EtcdPrefixes,
    start_revision: i64,
    sender: tokio::sync::mpsc::UnboundedSender<ConfigEvent>,
) -> Result<i64> {
    let domain_prefix = prefixes.domain_prefix.clone();
    let cluster_prefix = prefixes.cluster_prefix.clone();
    let meta_key = prefixes.meta_revision_key.clone();

    let client_d = client.clone();
    let client_c = client.clone();
    let client_m = client.clone();
    let sender_d = sender.clone();
    let sender_c = sender.clone();
    let sender_m = sender;

    let domain_handle = tokio::spawn(async move {
        watch_prefix_stream(&client_d, &domain_prefix, start_revision, sender_d, PrefixKind::Domain).await
    });

    let cluster_handle = tokio::spawn(async move {
        watch_prefix_stream(&client_c, &cluster_prefix, start_revision, sender_c, PrefixKind::Cluster).await
    });

    let meta_handle = tokio::spawn(async move {
        watch_meta_stream(&client_m, &meta_key, sender_m).await
    });

    // Wait for any to complete (stream end or error). Others will be cancelled.
    tokio::select! {
        result = domain_handle => {
            match result {
                Ok(Ok(rev)) => Ok(rev),
                Ok(Err(e)) => Err(e),
                Err(e) => Err(anyhow::anyhow!("domain watch task panicked: {}", e)),
            }
        }
        result = cluster_handle => {
            match result {
                Ok(Ok(rev)) => Ok(rev),
                Ok(Err(e)) => Err(e),
                Err(e) => Err(anyhow::anyhow!("cluster watch task panicked: {}", e)),
            }
        }
        result = meta_handle => {
            match result {
                Ok(Ok(rev)) => Ok(rev),
                Ok(Err(e)) => Err(e),
                Err(e) => Err(anyhow::anyhow!("meta watch task panicked: {}", e)),
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

fn normalize_prefix(prefix: &str) -> String {
    if prefix.ends_with('/') {
        prefix.to_string()
    } else {
        format!("{}/", prefix)
    }
}

async fn load_prefix<T: serde::de::DeserializeOwned + std::fmt::Debug>(
    client: &EtcdClient,
    prefix: &str,
    kind_label: &str,
) -> Result<(Vec<T>, i64)> {
    let key_b64 = b64_encode(prefix);
    let range_end = prefix_range_end(prefix);

    let resp = client
        .range(&RangeRequest {
            key: key_b64,
            range_end,
            keys_only: None,
        })
        .await?;

    let revision = resp
        .header
        .as_ref()
        .and_then(|h| h.revision)
        .unwrap_or(0);

    let mut items = Vec::new();
    for kv in &resp.kvs {
        let key_str = match b64_decode(&kv.key) {
            Ok(k) => k,
            Err(_) => continue,
        };

        if key_str.contains("/history/") {
            continue;
        }

        if let Ok(value) = b64_decode(&kv.value) {
            match serde_json::from_str::<T>(&value) {
                Ok(item) => items.push(item),
                Err(e) => {
                    warn!(
                        "etcd: initial {} parse failed, key={}, error={}",
                        kind_label, key_str, e
                    );
                }
            }
        }
    }

    info!(
        "etcd: initial {}s loaded, count={}, revision={}",
        kind_label,
        items.len(),
        revision
    );

    Ok((items, revision))
}

/// Read the controlplane config revision from etcd meta key.
async fn read_meta_revision(client: &EtcdClient, key: &str) -> i64 {
    let resp = client
        .range(&RangeRequest {
            key: b64_encode(key),
            range_end: String::new(),
            keys_only: None,
        })
        .await;

    match resp {
        Ok(r) => {
            if let Some(kv) = r.kvs.first() {
                if let Ok(val_str) = b64_decode(&kv.value) {
                    val_str.trim().parse::<i64>().unwrap_or(0)
                } else {
                    0
                }
            } else {
                0
            }
        }
        Err(e) => {
            warn!("etcd: failed to read meta config_revision: {}", e);
            0
        }
    }
}

#[derive(Clone, Copy)]
enum PrefixKind {
    Domain,
    Cluster,
}

async fn watch_prefix_stream(
    client: &EtcdClient,
    prefix: &str,
    start_revision: i64,
    sender: tokio::sync::mpsc::UnboundedSender<ConfigEvent>,
    kind: PrefixKind,
) -> Result<i64> {
    let key_b64 = b64_encode(prefix);
    let range_end = prefix_range_end(prefix);

    let mut stream = client
        .watch_stream(&WatchCreateRequest {
            create_request: WatchCreate {
                key: key_b64,
                range_end,
                start_revision: if start_revision > 0 {
                    Some(start_revision + 1)
                } else {
                    None
                },
            },
        })
        .await?;

    let mut latest_revision = start_revision;

    while let Some(watch_resp) = stream.next_response().await {
        if let Some(result) = watch_resp.result {
            if let Some(header) = &result.header {
                if let Some(rev) = header.revision {
                    latest_revision = rev;
                }
            }

            for event in &result.events {
                let event_type = event.event_type.as_deref().unwrap_or("PUT");

                match event_type {
                    "PUT" => {
                        if let Some(kv) = &event.kv {
                            let key_str = match b64_decode(&kv.key) {
                                Ok(k) => k,
                                Err(_) => continue,
                            };

                            if key_str.contains("/history/") {
                                continue;
                            }

                            if let Ok(value) = b64_decode(&kv.value) {
                                match kind {
                                    PrefixKind::Domain => {
                                        match serde_json::from_str::<DomainConfig>(&value) {
                                            Ok(domain) => {
                                                info!(
                                                    "etcd: watch: domain upserted, name={}, revision={}",
                                                    domain.name, latest_revision
                                                );
                                                let _ = sender.send(ConfigEvent::DomainUpsert(domain));
                                            }
                                            Err(e) => {
                                                error!(
                                                    "etcd: watch: domain parse failed, key={}, error={}",
                                                    key_str, e
                                                );
                                                let _ = sender.send(ConfigEvent::ParseError {
                                                    prefix_kind: "domain",
                                                    key: key_str,
                                                    error: e.to_string(),
                                                });
                                            }
                                        }
                                    }
                                    PrefixKind::Cluster => {
                                        match serde_json::from_str::<ClusterConfig>(&value) {
                                            Ok(cluster) => {
                                                info!(
                                                    "etcd: watch: cluster upserted, name={}, revision={}",
                                                    cluster.name, latest_revision
                                                );
                                                let _ = sender.send(ConfigEvent::ClusterUpsert(cluster));
                                            }
                                            Err(e) => {
                                                error!(
                                                    "etcd: watch: cluster parse failed, key={}, error={}",
                                                    key_str, e
                                                );
                                                let _ = sender.send(ConfigEvent::ParseError {
                                                    prefix_kind: "cluster",
                                                    key: key_str,
                                                    error: e.to_string(),
                                                });
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                    "DELETE" => {
                        if let Some(kv) = &event.kv {
                            let key_str = match b64_decode(&kv.key) {
                                Ok(k) => k,
                                Err(_) => continue,
                            };

                            if key_str.contains("/history/") {
                                continue;
                            }

                            let resource_name = key_str.strip_prefix(prefix).unwrap_or(&key_str);
                            if !resource_name.is_empty() {
                                match kind {
                                    PrefixKind::Domain => {
                                        info!(
                                            "etcd: watch: domain deleted, name={}, revision={}",
                                            resource_name, latest_revision
                                        );
                                        let _ = sender.send(ConfigEvent::DomainDelete(resource_name.to_string()));
                                    }
                                    PrefixKind::Cluster => {
                                        info!(
                                            "etcd: watch: cluster deleted, name={}, revision={}",
                                            resource_name, latest_revision
                                        );
                                        let _ = sender.send(ConfigEvent::ClusterDelete(resource_name.to_string()));
                                    }
                                }
                            }
                        }
                    }
                    _ => {}
                }
            }
        }
    }

    Ok(latest_revision)
}

/// Watch the meta config_revision key and send MetaRevision events.
async fn watch_meta_stream(
    client: &EtcdClient,
    key: &str,
    sender: tokio::sync::mpsc::UnboundedSender<ConfigEvent>,
) -> Result<i64> {
    let key_b64 = b64_encode(key);

    let mut stream = client
        .watch_stream(&WatchCreateRequest {
            create_request: WatchCreate {
                key: key_b64,
                range_end: String::new(), // exact key watch
                start_revision: None,
            },
        })
        .await?;

    let mut latest_revision: i64 = 0;

    while let Some(watch_resp) = stream.next_response().await {
        if let Some(result) = watch_resp.result {
            if let Some(header) = &result.header {
                if let Some(rev) = header.revision {
                    latest_revision = rev;
                }
            }

            for event in &result.events {
                let event_type = event.event_type.as_deref().unwrap_or("PUT");
                if event_type == "PUT" {
                    if let Some(kv) = &event.kv {
                        if let Ok(val_str) = b64_decode(&kv.value) {
                            if let Ok(cp_rev) = val_str.trim().parse::<i64>() {
                                let _ = sender.send(ConfigEvent::MetaRevision(cp_rev));
                            }
                        }
                    }
                }
            }
        }
    }

    Ok(latest_revision)
}
