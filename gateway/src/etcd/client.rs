use base64::Engine;
use bytes::BytesMut;
use futures_util::StreamExt;
use serde::{Deserialize, Deserializer, Serialize};
use tracing::error;

use crate::config::EtcdConfig;

/// Deserialize an i64 that may come as a JSON number or a JSON string (etcd v3.6+ gRPC-Gateway v2).
fn deserialize_i64_or_string<'de, D>(deserializer: D) -> Result<Option<i64>, D::Error>
where
    D: Deserializer<'de>,
{
    use serde::de;

    #[derive(Deserialize)]
    #[serde(untagged)]
    enum I64OrString {
        Num(i64),
        Str(String),
    }

    Option::<I64OrString>::deserialize(deserializer).and_then(|opt| match opt {
        None => Ok(None),
        Some(I64OrString::Num(n)) => Ok(Some(n)),
        Some(I64OrString::Str(s)) => s.parse::<i64>().map(Some).map_err(de::Error::custom),
    })
}

/// Shared etcd v3 HTTP/JSON client (avoids protoc/gRPC dependency).
///
/// Uses the gRPC-Gateway endpoints (`/v3/kv/range`, `/v3/kv/put`,
/// `/v3/watch`, `/v3/lease/*`, `/v3/auth/authenticate`).
///
/// Cheaply cloneable — the underlying `reqwest::Client` uses an `Arc`
/// internally so cloning just bumps a reference count.
#[derive(Clone)]
pub struct EtcdClient {
    http: reqwest::Client,
    base_url: String,
    auth_token: Option<String>,
}

#[derive(Serialize)]
struct AuthRequest {
    name: String,
    password: String,
}

#[derive(Deserialize)]
struct AuthResponse {
    token: Option<String>,
}

#[derive(Serialize)]
pub struct RangeRequest {
    pub key: String,
    pub range_end: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub keys_only: Option<bool>,
}

#[derive(Deserialize)]
pub struct RangeResponse {
    #[serde(default)]
    pub kvs: Vec<KeyValue>,
    #[serde(default)]
    pub header: Option<ResponseHeader>,
}

#[derive(Deserialize)]
pub struct ResponseHeader {
    #[serde(default, deserialize_with = "deserialize_i64_or_string")]
    pub revision: Option<i64>,
}

#[derive(Deserialize)]
pub struct KeyValue {
    pub key: String,
    #[serde(default)]
    pub value: String,
    #[serde(default, deserialize_with = "deserialize_i64_or_string")]
    pub mod_revision: Option<i64>,
}

#[derive(Serialize)]
pub struct PutRequest {
    pub key: String,
    pub value: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lease: Option<i64>,
}

#[derive(Serialize)]
pub struct WatchCreateRequest {
    pub create_request: WatchCreate,
}

#[derive(Serialize)]
pub struct WatchCreate {
    pub key: String,
    pub range_end: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub start_revision: Option<i64>,
}

#[derive(Deserialize)]
pub struct WatchResponse {
    #[serde(default)]
    pub result: Option<WatchResult>,
}

#[derive(Deserialize)]
pub struct WatchResult {
    #[serde(default)]
    pub events: Vec<WatchEvent>,
    #[serde(default)]
    pub header: Option<ResponseHeader>,
}

#[derive(Deserialize)]
pub struct WatchEvent {
    #[serde(rename = "type", default)]
    pub event_type: Option<String>,
    pub kv: Option<KeyValue>,
}

#[derive(Serialize)]
pub struct LeaseGrantRequest {
    #[serde(rename = "TTL")]
    pub ttl: u64,
}

#[derive(Deserialize)]
pub struct LeaseGrantResponse {
    #[serde(rename = "ID")]
    pub id: Option<String>,
}

#[derive(Serialize)]
pub struct LeaseKeepAliveRequest {
    #[serde(rename = "ID")]
    pub id: i64,
}

#[derive(Deserialize)]
pub struct LeaseKeepAliveResponse {
    pub result: Option<LeaseKeepAliveResult>,
}

#[derive(Deserialize)]
pub struct LeaseKeepAliveResult {
    #[serde(rename = "TTL")]
    #[allow(dead_code)]
    pub ttl: Option<String>,
}

#[derive(Serialize)]
pub struct LeaseRevokeRequest {
    #[serde(rename = "ID")]
    pub id: i64,
}

pub fn b64_encode(s: &str) -> String {
    base64::engine::general_purpose::STANDARD.encode(s.as_bytes())
}

pub fn b64_decode(s: &str) -> anyhow::Result<String> {
    let bytes = base64::engine::general_purpose::STANDARD.decode(s)?;
    Ok(String::from_utf8(bytes)?)
}

pub fn prefix_range_end(prefix: &str) -> String {
    let mut end = prefix.as_bytes().to_vec();
    for i in (0..end.len()).rev() {
        if end[i] < 0xff {
            end[i] += 1;
            end.truncate(i + 1);
            return b64_encode(&String::from_utf8_lossy(&end));
        }
    }
    String::new()
}

impl EtcdClient {
    /// Connect to etcd, trying each endpoint in order until one succeeds.
    /// Authenticates if credentials are provided.
    pub async fn connect(cfg: &EtcdConfig) -> anyhow::Result<Self> {
        if cfg.endpoints.is_empty() {
            anyhow::bail!("etcd: no endpoints configured");
        }

        let http = reqwest::Client::new();
        let mut last_error: Option<anyhow::Error> = None;

        for endpoint in &cfg.endpoints {
            let base_url = endpoint.trim_end_matches('/').to_string();

            let auth_token = if let (Some(user), Some(pass)) = (&cfg.username, &cfg.password) {
                match http
                    .post(format!("{}/v3/auth/authenticate", base_url))
                    .json(&AuthRequest {
                        name: user.clone(),
                        password: pass.clone(),
                    })
                    .send()
                    .await
                {
                    Ok(resp) => {
                        let auth: AuthResponse = resp.json().await?;
                        auth.token
                    }
                    Err(e) => {
                        tracing::warn!("etcd: endpoint {} auth failed: {}, trying next", base_url, e);
                        last_error = Some(e.into());
                        continue;
                    }
                }
            } else {
                // Verify connectivity with a lightweight request.
                match http.post(format!("{}/v3/kv/range", base_url))
                    .json(&RangeRequest {
                        key: base64::Engine::encode(&base64::engine::general_purpose::STANDARD, b"/"),
                        range_end: String::new(),
                        keys_only: Some(true),
                    })
                    .send()
                    .await
                {
                    Ok(_) => None,
                    Err(e) => {
                        tracing::warn!("etcd: endpoint {} unreachable: {}, trying next", base_url, e);
                        last_error = Some(e.into());
                        continue;
                    }
                }
            };

            return Ok(Self {
                http,
                base_url,
                auth_token,
            });
        }

        Err(last_error.unwrap_or_else(|| anyhow::anyhow!("etcd: all endpoints failed")))
    }

    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    /// Internal helper: POST JSON to an etcd endpoint with optional auth token.
    /// Returns the raw `reqwest::Response` on success, or an error with context.
    async fn post_json(
        &self,
        path: &str,
        body: &impl serde::Serialize,
    ) -> anyhow::Result<reqwest::Response> {
        let url = format!("{}{}", self.base_url, path);
        let mut req = self.http.post(&url).json(body);
        if let Some(ref token) = self.auth_token {
            req = req.header("Authorization", token);
        }
        let resp = req.send().await?;
        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            anyhow::bail!("etcd {} failed: {} - {}", path, status, body);
        }
        Ok(resp)
    }

    /// KV range query.
    pub async fn range(&self, req: &RangeRequest) -> anyhow::Result<RangeResponse> {
        Ok(self.post_json("/v3/kv/range", req).await?.json().await?)
    }

    /// KV put.
    pub async fn put(&self, req: &PutRequest) -> anyhow::Result<()> {
        self.post_json("/v3/kv/put", req).await?;
        Ok(())
    }

    /// Open a watch stream. Returns a receiver of parsed `WatchResponse` lines.
    /// The caller should loop on the receiver until it closes (stream ended / error).
    pub async fn watch_stream(
        &self,
        req: &WatchCreateRequest,
    ) -> anyhow::Result<WatchStream> {
        let resp = self.post_json("/v3/watch", req).await?;
        Ok(WatchStream {
            stream: Box::pin(resp.bytes_stream()),
            buf: BytesMut::with_capacity(4096),
        })
    }

    /// Grant a lease.
    pub async fn lease_grant(&self, ttl: u64) -> anyhow::Result<i64> {
        let grant: LeaseGrantResponse = self
            .post_json("/v3/lease/grant", &LeaseGrantRequest { ttl })
            .await?
            .json()
            .await?;
        let id: i64 = grant.id.unwrap_or_default().parse().unwrap_or(0);
        if id == 0 {
            anyhow::bail!("lease grant returned invalid ID");
        }
        Ok(id)
    }

    /// Keep a lease alive (single ping).
    pub async fn lease_keepalive(&self, lease_id: i64) -> anyhow::Result<()> {
        let ka: LeaseKeepAliveResponse = self
            .post_json("/v3/lease/keepalive", &LeaseKeepAliveRequest { id: lease_id })
            .await?
            .json()
            .await?;
        if ka.result.is_none() {
            anyhow::bail!("lease expired or not found");
        }
        Ok(())
    }

    /// Revoke a lease.
    pub async fn lease_revoke(&self, lease_id: i64) -> anyhow::Result<()> {
        self.post_json("/v3/lease/revoke", &LeaseRevokeRequest { id: lease_id }).await?;
        Ok(())
    }
}

/// A streaming watch connection. Call `next_event()` to get parsed responses.
pub struct WatchStream {
    stream: std::pin::Pin<Box<dyn futures_util::Stream<Item = Result<bytes::Bytes, reqwest::Error>> + Send>>,
    buf: BytesMut,
}

impl WatchStream {
    /// Read the next `WatchResponse` from the stream.
    /// Returns `None` when the stream ends.
    pub async fn next_response(&mut self) -> Option<WatchResponse> {
        loop {
            // Try to parse a complete line from the buffer first.
            if let Some(pos) = self.buf.iter().position(|&b| b == b'\n') {
                let line_bytes = self.buf.split_to(pos + 1);
                let line = String::from_utf8_lossy(&line_bytes).trim().to_string();
                if line.is_empty() {
                    continue;
                }
                match serde_json::from_str::<WatchResponse>(&line) {
                    Ok(resp) => return Some(resp),
                    Err(e) => {
                        error!("etcd: watch response parse failed: {}, line={}", e, line);
                        continue;
                    }
                }
            }

            // Need more data from the stream.
            match self.stream.next().await {
                Some(Ok(chunk)) => {
                    self.buf.extend_from_slice(&chunk);
                }
                Some(Err(e)) => {
                    error!("etcd: watch stream error: {}", e);
                    return None;
                }
                None => {
                    // Process any trailing data.
                    if !self.buf.is_empty() {
                        let line = String::from_utf8_lossy(&self.buf).trim().to_string();
                        self.buf.clear();
                        if !line.is_empty() {
                            if let Ok(resp) = serde_json::from_str::<WatchResponse>(&line) {
                                return Some(resp);
                            }
                        }
                    }
                    return None;
                }
            }
        }
    }
}
