use crate::routing::CompiledRoute;
use bytes::Bytes;
use http::StatusCode;
use http_body_util::{BodyExt, Full};
use std::net::IpAddr;
use std::sync::Arc;
use std::time::Instant;

pub type BoxBody = http_body_util::combinators::BoxBody<Bytes, hyper::Error>;

pub fn full_body(data: impl Into<Bytes>) -> BoxBody {
    Full::new(data.into())
        .map_err(|never| match never {})
        .boxed()
}

pub fn empty_body() -> BoxBody {
    Full::new(Bytes::new())
        .map_err(|never| match never {})
        .boxed()
}

/// Per-request context that flows through all phases.
/// Analogous to nginx's `ngx_http_request_t` — carries request metadata
/// and accumulates state across the filter chain.
pub struct RequestContext {
    pub host: String,
    pub uri_path: String,
    pub method: String,
    pub route_name: String,
    pub upstream_addr: String,
    /// The downstream client IP address (from TCP peer or trusted X-Forwarded-For).
    pub client_ip: IpAddr,
    pub start: Instant,
    pub upstream_start: Option<Instant>,
    pub route: Option<Arc<CompiledRoute>>,
}

impl RequestContext {
    pub fn new(host: String, uri_path: String, method: String, client_ip: IpAddr) -> Self {
        Self {
            host,
            uri_path,
            method,
            route_name: String::new(),
            upstream_addr: String::new(),
            client_ip,
            start: Instant::now(),
            upstream_start: None,
            route: None,
        }
    }

    /// Build a JSON error response and record metrics in one place.
    /// This is the single exit point for all error paths — eliminates
    /// the 5x duplicated metrics + response-building code.
    pub fn error_response(&self, status: StatusCode, msg: &str) -> hyper::Response<BoxBody> {
        let mut buf = itoa::Buffer::new();
        let status_str = buf.format(status.as_u16());

        metrics::counter!(
            "gateway_http_requests_total",
            "route" => self.route_name.clone(),
            "method" => self.method.clone(),
            "status_code" => status_str.to_owned(),
            "upstream_addr" => self.upstream_addr.clone(),
        )
        .increment(1);

        metrics::histogram!(
            "gateway_http_request_duration_seconds",
            "route" => self.route_name.clone(),
            "upstream_addr" => self.upstream_addr.clone(),
        )
        .record(self.start.elapsed().as_secs_f64());

        if !self.route_name.is_empty() {
            metrics::gauge!(
                "gateway_http_requests_in_flight",
                "route" => self.route_name.clone(),
            )
            .decrement(1.0);
        }

        if let Some(upstream_start) = self.upstream_start {
            metrics::histogram!(
                "gateway_upstream_request_duration_seconds",
                "route" => self.route_name.clone(),
                "upstream_addr" => self.upstream_addr.clone(),
            )
            .record(upstream_start.elapsed().as_secs_f64());
        }

        hyper::Response::builder()
            .status(status)
            .header("content-type", "application/json")
            .body(full_body(format!(r#"{{"error":"{}"}}"#, msg)))
            .unwrap()
    }

    /// Record final metrics for a successful response.
    pub fn finalize_metrics(&self, resp_status: u16) {
        let mut buf = itoa::Buffer::new();
        let status_str = buf.format(resp_status);

        metrics::counter!(
            "gateway_http_requests_total",
            "route" => self.route_name.clone(),
            "method" => self.method.clone(),
            "status_code" => status_str.to_owned(),
            "upstream_addr" => self.upstream_addr.clone(),
        )
        .increment(1);

        metrics::histogram!(
            "gateway_http_request_duration_seconds",
            "route" => self.route_name.clone(),
            "upstream_addr" => self.upstream_addr.clone(),
        )
        .record(self.start.elapsed().as_secs_f64());

        if let Some(upstream_start) = self.upstream_start {
            metrics::histogram!(
                "gateway_upstream_request_duration_seconds",
                "route" => self.route_name.clone(),
                "upstream_addr" => self.upstream_addr.clone(),
            )
            .record(upstream_start.elapsed().as_secs_f64());
        }

        metrics::gauge!(
            "gateway_http_requests_in_flight",
            "route" => self.route_name.clone(),
        )
        .decrement(1.0);
    }
}
