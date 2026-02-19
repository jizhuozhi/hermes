use crate::config::ActiveHealthCheck;
use crate::upstream::cluster::{Cluster, ClusterStore};
use futures_util::stream::{self, StreamExt};
use std::sync::Arc;
use std::time::Duration;
use tracing::{debug, warn};

/// Run a single round of active health checks across all clusters.
///
/// The caller is responsible for looping / scheduling.
pub async fn run_health_checks(cluster_store: &ClusterStore, client: &reqwest::Client) {
    let mut tasks: Vec<(
        Cluster,
        Arc<ActiveHealthCheck>,
        Vec<crate::config::UpstreamNode>,
    )> = Vec::new();

    cluster_store.for_each(|_name, cluster| {
        let cfg = cluster.config();
        let hc = match &cfg.health_check {
            Some(hc) => hc,
            None => return,
        };

        let active = match &hc.active {
            Some(a) => a,
            None => return,
        };

        let nodes = cluster.effective_nodes();
        if nodes.is_empty() {
            return;
        }

        tasks.push((cluster.clone(), Arc::new(active.clone()), nodes));
    });

    for (cluster, active, nodes) in tasks {
        let concurrency = active.concurrency;

        stream::iter(nodes)
            .map(|node| {
                let client = client.clone();
                let cluster = cluster.clone();
                let active = active.clone();
                async move {
                    check_one_node(&client, &cluster, &active, &node).await;
                }
            })
            .buffer_unordered(concurrency)
            .collect::<()>()
            .await;
    }
}

/// Build a shared HTTP client for health checks.
pub fn build_health_check_client() -> reqwest::Client {
    reqwest::Client::builder()
        .timeout(Duration::from_secs(5))
        .no_proxy()
        .build()
        .expect("failed to build health check client")
}

async fn check_one_node(
    client: &reqwest::Client,
    cluster: &Cluster,
    active: &ActiveHealthCheck,
    node: &crate::config::UpstreamNode,
) {
    let url = format!(
        "{}://{}:{}{}",
        cluster.config().scheme,
        node.host,
        node.port,
        active.path
    );
    let node_key = format!("{}:{}", node.host, node.port);
    let cluster_name = cluster.name();

    let result = client
        .get(&url)
        .timeout(Duration::from_secs(active.timeout))
        .send()
        .await;

    let healthy = match result {
        Ok(resp) => {
            let status = resp.status().as_u16();
            active.healthy_statuses.contains(&status)
        }
        Err(_) => false,
    };

    if healthy {
        let count = cluster.record_health_success(&node_key);
        if count >= active.healthy_threshold {
            cluster.mark_node_healthy(&node_key);
        }
        debug!(
            "health: active: check passed, cluster={}, node={}",
            cluster_name, node_key
        );
    } else {
        let count = cluster.record_health_failure(&node_key);
        if count >= active.unhealthy_threshold {
            cluster.mark_node_unhealthy(&node_key);
            warn!(
                "health: active: node marked unhealthy, cluster={}, node={}, consecutive_failures={}",
                cluster_name, node_key, count
            );
        }
        debug!(
            "health: active: check failed, cluster={}, node={}",
            cluster_name, node_key
        );
    }
}
