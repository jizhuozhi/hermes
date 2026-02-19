use crate::error::GatewayError;
use serde::{Deserialize, Deserializer, Serialize};
use std::collections::HashMap;

/// Deserialize a `T` that implements `Default` — treats JSON `null` the same as
/// a missing field (returns `T::default()`).
fn deserialize_null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

/// Consul /v1/health/service response — Service structure.
#[derive(Debug, Clone, Deserialize)]
pub struct ConsulService {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "Address")]
    pub address: String,
    #[serde(rename = "Port")]
    pub port: u16,
    #[serde(
        rename = "Meta",
        default,
        deserialize_with = "deserialize_null_default"
    )]
    pub meta: HashMap<String, String>,
}

/// Consul health check structure.
#[derive(Debug, Clone, Deserialize)]
pub struct ConsulCheck {
    #[serde(rename = "CheckID")]
    pub check_id: String,
    #[serde(rename = "Status")]
    pub status: String,
}

/// Consul node structure.
#[derive(Debug, Clone, Deserialize)]
pub struct ConsulNode {
    #[serde(rename = "Node")]
    pub node: String,
}

/// A single entry from the /v1/health/service response.
#[derive(Debug, Deserialize)]
struct ConsulHealthEntry {
    #[serde(rename = "Node")]
    node: ConsulNode,
    #[serde(rename = "Service")]
    service: ConsulService,
    #[serde(rename = "Checks", default)]
    checks: Vec<ConsulCheck>,
}

/// Exposed service node information.
#[derive(Debug, Clone)]
pub struct ConsulServiceNode {
    pub service_id: String,
    pub service_address: String,
    pub service_port: u16,
    pub service_meta: HashMap<String, String>,
}

impl From<ConsulService> for ConsulServiceNode {
    fn from(svc: ConsulService) -> Self {
        Self {
            service_id: svc.id,
            service_address: if svc.address.is_empty() {
                "127.0.0.1".to_string()
            } else {
                svc.address
            },
            service_port: svc.port,
            service_meta: svc.meta,
        }
    }
}

/// Consul HTTP client.
#[derive(Clone)]
pub struct ConsulClient {
    base_url: String,
    client: reqwest::Client,
    token: Option<String>,
    datacenter: Option<String>,
}

impl ConsulClient {
    pub fn new(consul_addr: &str, token: Option<String>, datacenter: Option<String>) -> Self {
        let base_url = if consul_addr.starts_with("http://") || consul_addr.starts_with("https://")
        {
            consul_addr.trim_end_matches('/').to_string()
        } else {
            format!("http://{}", consul_addr.trim_end_matches('/'))
        };

        let client = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(10))
            .connect_timeout(std::time::Duration::from_secs(5))
            .build()
            .expect("failed to build consul HTTP client");

        Self {
            base_url,
            client,
            token,
            datacenter,
        }
    }

    /// Inject the Consul ACL token into a request builder if configured.
    fn authed(&self, req: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        match &self.token {
            Some(token) => req.header("X-Consul-Token", token),
            None => req,
        }
    }

    /// Query all healthy instances of a service.
    /// Uses `?passing=true` and additionally filters out nodes with critical serfHealth.
    pub async fn query_healthy_services(
        &self,
        service_name: &str,
    ) -> Result<Vec<ConsulServiceNode>, GatewayError> {
        let mut url = format!(
            "{}/v1/health/service/{}?passing=true",
            self.base_url, service_name
        );

        if let Some(dc) = &self.datacenter {
            url.push_str(&format!("&dc={}", dc));
        }

        let resp = self
            .authed(self.client.get(&url))
            .send()
            .await
            .map_err(GatewayError::Http)?;

        if !resp.status().is_success() {
            return Err(GatewayError::Consul(format!(
                "non-200 response: status={}",
                resp.status()
            )));
        }

        let entries: Vec<ConsulHealthEntry> = resp.json().await.map_err(GatewayError::Http)?;

        let nodes: Vec<ConsulServiceNode> = entries
            .into_iter()
            .filter(|entry| {
                let has_critical_serf = entry.checks.iter().any(|check| {
                    check.check_id == "serfHealth" && check.status == "critical"
                });
                if has_critical_serf {
                    tracing::warn!(
                        "discovery: consul: skipping node with critical serfHealth, node={}, service={}",
                        entry.node.node,
                        service_name
                    );
                    false
                } else {
                    true
                }
            })
            .map(|entry| entry.service.into())
            .collect();

        Ok(nodes)
    }

    /// Register service to Consul via /v1/agent/service/register.
    pub async fn register_service<T: Serialize>(
        &self,
        registration: &T,
    ) -> Result<(), GatewayError> {
        let url = format!("{}/v1/agent/service/register", self.base_url);

        let resp = self
            .authed(self.client.put(&url).json(registration))
            .send()
            .await
            .map_err(GatewayError::Http)?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            return Err(GatewayError::Consul(format!(
                "register failed: {} - {}",
                status, body
            )));
        }

        Ok(())
    }

    /// Deregister service from Consul via /v1/agent/service/deregister/{service_id}.
    pub async fn deregister_service(&self, service_id: &str) -> Result<(), GatewayError> {
        let url = format!(
            "{}/v1/agent/service/deregister/{}",
            self.base_url, service_id
        );

        let resp = self
            .authed(self.client.put(&url))
            .send()
            .await
            .map_err(GatewayError::Http)?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            tracing::warn!("consul: deregister failed: {} - {}", status, body);
        }

        Ok(())
    }

    /// Send TTL heartbeat via /v1/agent/check/pass/{check_id}.
    pub async fn pass_ttl(&self, check_id: &str) -> Result<(), GatewayError> {
        let url = format!("{}/v1/agent/check/pass/{}", self.base_url, check_id);

        let resp = self
            .authed(self.client.put(&url))
            .send()
            .await
            .map_err(GatewayError::Http)?;

        if resp.status().as_u16() == 404 {
            return Err(GatewayError::Consul("service not found".to_string()));
        }

        if !resp.status().is_success() {
            let status = resp.status();
            return Err(GatewayError::Consul(format!("pass TTL failed: {}", status)));
        }

        Ok(())
    }
}
