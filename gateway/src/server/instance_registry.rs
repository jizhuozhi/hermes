use std::sync::atomic::{AtomicI64, AtomicU32, Ordering};
use std::sync::Arc;
use tracing::{error, info, warn};

use crate::config::InstanceRegistryConfig;
use crate::etcd::client::{
    b64_encode, prefix_range_end, PutRequest, RangeRequest, WatchCreate, WatchCreateRequest,
};
use crate::etcd::EtcdClient;

/// Manages this gateway instance's registration in etcd for distributed rate limiting.
///
/// Provides pure API operations. The caller (bootstrap) owns the lifecycle loops.
///
/// Lifecycle:
/// 1. `new()` — prepare instance metadata
/// 2. `register()` — create lease, write instance key, count peers → ready
/// 3. `keepalive_once()` — single lease renewal (caller loops)
/// 4. `watch_instances_once()` — single watch session (caller loops on reconnect)
/// 5. `shutdown()` — revoke lease (key auto-deleted)
pub struct InstanceRegistry {
    etcd: EtcdClient,
    instance_id: String,
    key: String,
    prefix: String,
    lease_ttl: u64,
    lease_id: std::sync::Mutex<Option<i64>>,
    instance_count: Arc<AtomicU32>,
    /// Process start time (set once at construction).
    started_at: String,
    /// First successful registration time (set once in register()).
    first_registered_at: std::sync::Mutex<Option<String>>,
    /// Last successful lease keepalive time (updated every keepalive cycle).
    last_keepalive_at: std::sync::Mutex<String>,
    /// Latest config revision observed from etcd watch (updated externally).
    config_revision: AtomicI64,
    /// Current instance status: "starting", "running", "shutting_down".
    status: std::sync::Mutex<String>,
}

impl InstanceRegistry {
    pub fn new(
        etcd: EtcdClient,
        registry_cfg: &InstanceRegistryConfig,
        instance_count: Arc<AtomicU32>,
    ) -> Self {
        let instance_id = generate_instance_id();
        let prefix = registry_cfg.prefix.trim_end_matches('/').to_string();
        let key = format!("{}/{}", prefix, instance_id);
        let now = now_rfc3339();

        Self {
            etcd,
            instance_id,
            key,
            prefix,
            lease_ttl: registry_cfg.lease_ttl_secs,
            lease_id: std::sync::Mutex::new(None),
            instance_count,
            started_at: now.clone(),
            first_registered_at: std::sync::Mutex::new(None),
            last_keepalive_at: std::sync::Mutex::new(now),
            config_revision: AtomicI64::new(0),
            status: std::sync::Mutex::new("starting".to_string()),
        }
    }

    /// Register this instance: grant lease → put key → count peers.
    /// Returns the initial instance count. This MUST succeed before the
    /// gateway is considered ready to serve traffic.
    pub async fn register(&self) -> anyhow::Result<u32> {
        let lease_id = self.etcd.lease_grant(self.lease_ttl).await?;
        {
            let mut guard = self.lease_id.lock().unwrap();
            *guard = Some(lease_id);
        }

        // Record first registration time (only once).
        {
            let mut reg = self.first_registered_at.lock().unwrap();
            if reg.is_none() {
                *reg = Some(now_rfc3339());
            }
        }
        // Mark running.
        {
            let mut s = self.status.lock().unwrap();
            *s = "running".to_string();
        }

        self.put_instance_key(lease_id).await?;

        let count = self.count_instances().await?;
        self.instance_count.store(count, Ordering::Release);

        info!(
            "instance_registry: registered, id={}, lease={}, instances={}",
            self.instance_id, lease_id, count
        );

        Ok(count)
    }

    /// Perform a single keepalive cycle: renew lease + update instance key.
    /// Returns Ok(()) on success, or re-registers on failure.
    pub async fn keepalive_once(&self) -> anyhow::Result<()> {
        let lease_id = {
            let guard = self.lease_id.lock().unwrap();
            match *guard {
                Some(id) => id,
                None => anyhow::bail!("no active lease"),
            }
        };

        if let Err(e) = self.etcd.lease_keepalive(lease_id).await {
            error!(
                "instance_registry: keepalive failed, attempting re-register: {}",
                e
            );
            self.re_register().await?;
        } else {
            // Keepalive succeeded — update timestamp and re-put instance key.
            {
                let mut guard = self.last_keepalive_at.lock().unwrap();
                *guard = now_rfc3339();
            }
            if let Err(e) = self.put_instance_key(lease_id).await {
                warn!("instance_registry: failed to update instance key: {}", e);
            }
        }

        Ok(())
    }

    /// Open a single watch session on the instance prefix. Blocks until the
    /// stream ends or an error occurs. The caller should loop with backoff.
    pub async fn watch_instances_once(&self) {
        let prefix_with_slash = format!("{}/", self.prefix);

        let mut stream = match self
            .etcd
            .watch_stream(&WatchCreateRequest {
                create_request: WatchCreate {
                    key: b64_encode(&prefix_with_slash),
                    range_end: prefix_range_end(&prefix_with_slash),
                    start_revision: None,
                },
            })
            .await
        {
            Ok(s) => s,
            Err(e) => {
                error!("instance_registry: watch connect failed: {}", e);
                return;
            }
        };

        while let Some(watch_resp) = stream.next_response().await {
            if let Some(result) = watch_resp.result {
                if !result.events.is_empty() {
                    match self.count_instances().await {
                        Ok(count) => {
                            let old = self.instance_count.swap(count, Ordering::Release);
                            if old != count {
                                info!(
                                    "instance_registry: peer count changed, {} -> {}",
                                    old, count
                                );
                            }
                        }
                        Err(e) => {
                            warn!(
                                "instance_registry: failed to recount instances: {}",
                                e
                            );
                        }
                    }
                }
            }
        }
    }

    /// Keepalive interval = lease_ttl / 3.
    pub fn keepalive_interval(&self) -> std::time::Duration {
        std::time::Duration::from_secs(self.lease_ttl / 3)
    }

    /// Graceful shutdown: revoke lease (instance key auto-deleted by etcd).
    pub async fn shutdown(&self) {
        {
            let mut s = self.status.lock().unwrap();
            *s = "shutting_down".to_string();
        }

        let lease_id = {
            let guard = self.lease_id.lock().unwrap();
            *guard
        };

        if let Some(id) = lease_id {
            if let Err(e) = self.etcd.lease_revoke(id).await {
                warn!("instance_registry: lease revoke failed: {}", e);
            } else {
                info!("instance_registry: lease revoked, id={}", self.instance_id);
            }
        }
    }

    pub fn instance_id(&self) -> &str {
        &self.instance_id
    }

    /// Called by the config watcher whenever a new etcd revision is observed.
    pub fn set_config_revision(&self, revision: i64) {
        let old = self.config_revision.swap(revision, Ordering::Release);
        if old != revision {
            info!(
                "instance_registry: config_revision updated, {} -> {}",
                old, revision
            );
        }
    }

    async fn put_instance_key(&self, lease_id: i64) -> anyhow::Result<()> {
        let registered_at = {
            let guard = self.first_registered_at.lock().unwrap();
            guard.clone().unwrap_or_else(|| self.started_at.clone())
        };
        let last_keepalive = {
            let guard = self.last_keepalive_at.lock().unwrap();
            guard.clone()
        };
        let status = {
            let guard = self.status.lock().unwrap();
            guard.clone()
        };

        let value_json = serde_json::json!({
            "id": self.instance_id,
            "status": status,
            "started_at": self.started_at,
            "registered_at": registered_at,
            "last_keepalive_at": last_keepalive,
            "config_revision": self.config_revision.load(Ordering::Acquire),
        });

        self.etcd
            .put(&PutRequest {
                key: b64_encode(&self.key),
                value: b64_encode(&value_json.to_string()),
                lease: Some(lease_id),
            })
            .await
    }

    async fn count_instances(&self) -> anyhow::Result<u32> {
        let prefix_with_slash = format!("{}/", self.prefix);
        let resp = self
            .etcd
            .range(&RangeRequest {
                key: b64_encode(&prefix_with_slash),
                range_end: prefix_range_end(&prefix_with_slash),
                keys_only: Some(true),
            })
            .await?;
        Ok(resp.kvs.len().max(1) as u32)
    }

    async fn re_register(&self) -> anyhow::Result<()> {
        let lease_id = self.etcd.lease_grant(self.lease_ttl).await?;
        {
            let mut guard = self.lease_id.lock().unwrap();
            *guard = Some(lease_id);
        }
        self.put_instance_key(lease_id).await?;
        let count = self.count_instances().await?;
        self.instance_count.store(count, Ordering::Release);
        info!(
            "instance_registry: re-registered, id={}, instances={}",
            self.instance_id, count
        );
        Ok(())
    }
}

fn generate_instance_id() -> String {
    let hostname = hostname::get()
        .ok()
        .and_then(|h| h.into_string().ok())
        .unwrap_or_else(|| "unknown".to_string());
    let rand_suffix: u32 = rand::random();
    format!("{}-{:08x}", hostname, rand_suffix)
}

fn now_rfc3339() -> String {
    humantime::format_rfc3339_seconds(std::time::SystemTime::now()).to_string()
}
