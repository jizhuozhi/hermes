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
            tracing::info!("config file not found at {}, using defaults", path.display());
            GatewayConfig::default()
        };

        // Environment variable overrides for infrastructure settings.
        config.apply_env_overrides();

        config.validate()?;
        let total_routes: usize = config.domains.iter().map(|d| d.routes.len()).sum();
        tracing::info!(
            domains = config.domains.len(),
            total_routes = total_routes,
            "loaded gateway configuration"
        );
        Ok(config)
    }

    /// Apply environment variable overrides for connection/infra settings.
    /// Business config (domains, routes, clusters) should be managed via
    /// config files or the control plane — not environment variables.
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
        // Collect all known cluster names for cross-reference checking.
        let cluster_names: std::collections::HashSet<&str> =
            self.clusters.iter().map(|c| c.name.as_str()).collect();

        for domain in &self.domains {
            if domain.hosts.is_empty() {
                anyhow::bail!("domain '{}' has no hosts defined", domain.name);
            }
            for host in &domain.hosts {
                if host.is_empty() {
                    anyhow::bail!("domain '{}' has an empty host entry", domain.name);
                }
            }
            for route in &domain.routes {
                if route.uri.is_empty() {
                    anyhow::bail!(
                        "route '{}' in domain '{}' has empty uri",
                        route.name, domain.name
                    );
                }
                // Validate that referenced clusters exist.
                for wc in &route.clusters {
                    if !cluster_names.contains(wc.name.as_str()) {
                        anyhow::bail!(
                            "route '{}' in domain '{}' references unknown cluster '{}'",
                            route.name, domain.name, wc.name
                        );
                    }
                    if wc.weight == 0 {
                        anyhow::bail!(
                            "route '{}' in domain '{}' has cluster '{}' with weight 0",
                            route.name, domain.name, wc.name
                        );
                    }
                }
                // Validate rate_limit config consistency.
                if let Some(ref rl) = route.rate_limit {
                    if rl.mode == "count" || rl.mode == "sliding_window" {
                        if rl.count.is_none() || rl.time_window.is_none() {
                            anyhow::bail!(
                                "route '{}' in domain '{}': rate_limit mode '{}' requires 'count' and 'time_window'",
                                route.name, domain.name, rl.mode
                            );
                        }
                    }
                }
            }
        }
        Ok(())
    }

    /// Total route count across all domains.
    pub fn total_route_count(&self) -> usize {
        self.domains.iter().map(|d| d.routes.len()).sum()
    }
}

impl Default for GatewayConfig {
    fn default() -> Self {
        Self {
            consul: ConsulConfig::default(),
            etcd: EtcdConfig::default(),
            domains: Vec::new(),
            clusters: Vec::new(),
            registration: RegistrationConfig::default(),
            instance_registry: InstanceRegistryConfig::default(),
        }
    }
}
