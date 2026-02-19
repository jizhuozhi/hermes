pub mod etcd;
pub mod types;

#[cfg(test)]
mod tests;

pub use types::*;

use anyhow::Result;
use std::path::Path;

impl GatewayConfig {
    /// Load configuration from a file (if it exists) and apply environment
    /// variable overrides for infrastructure settings. When the file does not
    /// exist, built-in defaults are used — allowing the gateway to start with
    /// zero configuration for local development.
    pub fn load(path: &Path) -> Result<Self> {
        let mut config: GatewayConfig = if path.exists() {
            let content = std::fs::read_to_string(path)?;
            match path.extension().and_then(|e| e.to_str()) {
                Some("toml") => toml::from_str(&content)?,
                Some("json") => serde_json::from_str(&content)?,
                Some(ext) => anyhow::bail!("unsupported config format: .{ext}, use .toml or .json"),
                None => anyhow::bail!("config file has no extension, use .toml or .json"),
            }
        } else {
            tracing::info!(
                "config file not found at {}, using defaults",
                path.display()
            );
            GatewayConfig::default()
        };

        // Environment variable overrides for infrastructure settings.
        config.apply_env_overrides();

        config.validate()?;
        tracing::info!("loaded gateway infrastructure configuration");
        Ok(config)
    }

    /// Apply environment variable overrides for connection/infra settings.
    /// Business config (domains, routes, clusters) is managed exclusively
    /// via the control plane (etcd) — never from local files or env vars.
    fn apply_env_overrides(&mut self) {
        // Consul
        if let Ok(v) = std::env::var("HERMES_CONSUL_ADDRESS") {
            self.consul.address = v;
        }
        if let Ok(v) = std::env::var("HERMES_CONSUL_DATACENTER") {
            self.consul.datacenter = Some(v);
        }
        if let Ok(v) = std::env::var("HERMES_CONSUL_TOKEN") {
            self.consul.token = Some(v);
        }
        if let Ok(v) = std::env::var("HERMES_CONSUL_POLL_INTERVAL") {
            if let Ok(n) = v.parse::<u64>() {
                self.consul.poll_interval_secs = n;
            }
        }

        // etcd
        if let Ok(v) = std::env::var("HERMES_ETCD_ENDPOINTS") {
            self.etcd.endpoints = v.split(',').map(|s| s.trim().to_string()).collect();
        }
        if let Ok(v) = std::env::var("HERMES_ETCD_DOMAIN_PREFIX") {
            self.etcd.domain_prefix = v;
        }
        if let Ok(v) = std::env::var("HERMES_ETCD_CLUSTER_PREFIX") {
            self.etcd.cluster_prefix = v;
        }
        if let Ok(v) = std::env::var("HERMES_ETCD_META_PREFIX") {
            self.etcd.meta_prefix = Some(v);
        }
        if let Ok(v) = std::env::var("HERMES_ETCD_USERNAME") {
            self.etcd.username = Some(v);
        }
        if let Ok(v) = std::env::var("HERMES_ETCD_PASSWORD") {
            self.etcd.password = Some(v);
        }

        // Instance registry
        if let Ok(v) = std::env::var("HERMES_INSTANCE_REGISTRY_ENABLED") {
            self.instance_registry.enabled = v == "true" || v == "1";
        }
        if let Ok(v) = std::env::var("HERMES_INSTANCE_REGISTRY_PREFIX") {
            self.instance_registry.prefix = v;
        }

        // Registration
        if let Ok(v) = std::env::var("HERMES_REGISTRATION_ENABLED") {
            self.registration.enabled = v == "true" || v == "1";
        }
        if let Ok(v) = std::env::var("HERMES_REGISTRATION_SERVICE_NAME") {
            self.registration.service_name = v;
        }
    }

    pub fn validate(&self) -> Result<()> {
        // Infrastructure-only validation.
        // Business config (domains, clusters, routes) is loaded exclusively
        // from etcd and validated at the control-plane level.
        if !self.etcd.endpoints.is_empty() {
            for ep in &self.etcd.endpoints {
                if ep.is_empty() {
                    anyhow::bail!("etcd endpoint cannot be empty");
                }
            }
        }
        Ok(())
    }
}
