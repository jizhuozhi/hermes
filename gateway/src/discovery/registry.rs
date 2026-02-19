use super::client::ConsulClient;
use crate::config::RegistrationConfig;
use crate::error::GatewayError;
use serde::Serialize;
use std::collections::HashMap;

/// Consul service registrar — registers this gateway instance to Consul
/// so that upstream gateways / load balancers can discover it.
///
/// This struct provides pure API operations (register / deregister / heartbeat).
/// The caller (bootstrap) owns the lifecycle loop.
pub struct ConsulRegistry {
    client: ConsulClient,
    service_id: String,
    service_info: ServiceRegistration,
    config: RegistrationConfig,
}

#[derive(Debug, Clone, Serialize)]
struct ServiceRegistration {
    #[serde(rename = "ID")]
    id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(rename = "Address")]
    address: String,
    #[serde(rename = "Port")]
    port: u16,
    #[serde(rename = "Meta")]
    meta: HashMap<String, String>,
    #[serde(rename = "Check")]
    check: TTLCheck,
}

#[derive(Debug, Clone, Serialize)]
struct TTLCheck {
    #[serde(rename = "CheckID")]
    check_id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(rename = "TTL")]
    ttl: String,
    #[serde(rename = "DeregisterCriticalServiceAfter")]
    deregister_after: String,
}

impl ConsulRegistry {
    pub fn new(
        client: ConsulClient,
        listen_addr: &str,
        config: RegistrationConfig,
    ) -> Result<Self, GatewayError> {
        let (address, port) = Self::parse_listen_addr(listen_addr)?;

        let service_id = format!("{}:{}:{}", config.service_name, address, port);

        let service_info = ServiceRegistration {
            id: service_id.clone(),
            name: config.service_name.clone(),
            address: address.clone(),
            port,
            meta: config.metadata.clone(),
            check: TTLCheck {
                check_id: service_id.clone(),
                name: format!("Service '{}' TTL Status", config.service_name),
                ttl: format!("{}s", config.ttl_secs),
                deregister_after: format!("{}s", config.deregister_after_secs),
            },
        };

        Ok(Self {
            client,
            service_id,
            service_info,
            config,
        })
    }

    fn parse_listen_addr(listen_addr: &str) -> Result<(String, u16), GatewayError> {
        let parts: Vec<&str> = listen_addr.rsplitn(2, ':').collect();
        if parts.len() != 2 {
            return Err(GatewayError::Config(format!(
                "invalid listen_addr format: {}",
                listen_addr
            )));
        }

        let port: u16 = parts[0]
            .parse()
            .map_err(|_| GatewayError::Config(format!("invalid port: {}", parts[0])))?;

        let host = parts[1];
        let address = if host.is_empty() || host == "0.0.0.0" || host == "::" {
            Self::get_local_ip()?
        } else {
            host.to_string()
        };

        Ok((address, port))
    }

    fn get_local_ip() -> Result<String, GatewayError> {
        // Prefer K8s Pod IP from env.
        if let Ok(ip) = std::env::var("MY_POD_IP") {
            return Ok(ip);
        }
        if let Ok(ip) = std::env::var("POD_IP") {
            return Ok(ip);
        }
        if let Ok(ip) = std::env::var("HOST_IP") {
            return Ok(ip);
        }

        // Fallback: scan network interfaces.
        for iface in pnet_datalink::interfaces() {
            for ip in iface.ips {
                if let ipnetwork::IpNetwork::V4(ipv4) = ip {
                    let addr = ipv4.ip();
                    if !addr.is_loopback() && !addr.is_link_local() {
                        return Ok(addr.to_string());
                    }
                }
            }
        }

        Err(GatewayError::Config(
            "failed to determine local IP, set MY_POD_IP or HOST_IP env".to_string(),
        ))
    }

    /// Register service to Consul.
    pub async fn register(&self) -> Result<(), GatewayError> {
        tracing::info!(
            "consul: registering service {} ({}:{})",
            self.service_info.name,
            self.service_info.address,
            self.service_info.port
        );

        self.client.register_service(&self.service_info).await?;

        tracing::info!("consul: registered service {}", self.service_id);
        Ok(())
    }

    /// Deregister service from Consul.
    pub async fn deregister(&self) -> Result<(), GatewayError> {
        tracing::info!("consul: deregistering service {}", self.service_id);
        self.client.deregister_service(&self.service_id).await?;
        tracing::info!("consul: deregistered service {}", self.service_id);
        Ok(())
    }

    /// Send a single TTL heartbeat. On failure the caller should re-register.
    pub async fn pass_ttl(&self) -> Result<(), GatewayError> {
        self.client.pass_ttl(&self.service_id).await
    }

    /// Heartbeat interval = TTL / 2.
    pub fn heartbeat_interval(&self) -> std::time::Duration {
        std::time::Duration::from_secs(self.config.ttl_secs / 2)
    }

    pub fn service_id(&self) -> &str {
        &self.service_id
    }
}
