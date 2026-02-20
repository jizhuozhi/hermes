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

pub struct RequestContext {
    pub host: String,
    pub uri_path: String,
    pub method: String,
    pub domain_name: String,
    pub route_name: String,
    pub cluster_name: String,
    pub upstream_addr: String,
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
            domain_name: String::new(),
            route_name: String::new(),
            cluster_name: String::new(),
            upstream_addr: String::new(),
            client_ip,
            start: Instant::now(),
            upstream_start: None,
            route: None,
        }
    }

    pub fn error_response(&self, status: StatusCode, msg: &str) -> hyper::Response<BoxBody> {
        let mut buf = itoa::Buffer::new();
        let status_str = buf.format(status.as_u16());

        metrics::counter!(
            "gateway_http_requests_total",
            "domain" => self.domain_name.clone(),
            "route" => self.route_name.clone(),
            "cluster" => self.cluster_name.clone(),
            "method" => self.method.clone(),
            "status_code" => status_str.to_owned(),
            "upstream" => self.upstream_addr.clone(),
        )
        .increment(1);

        metrics::histogram!(
            "gateway_http_request_duration_seconds",
            "domain" => self.domain_name.clone(),
            "route" => self.route_name.clone(),
            "cluster" => self.cluster_name.clone(),
            "upstream" => self.upstream_addr.clone(),
        )
        .record(self.start.elapsed().as_secs_f64());

        if !self.route_name.is_empty() {
            metrics::gauge!(
                "gateway_http_requests_in_flight",
                "domain" => self.domain_name.clone(),
                "route" => self.route_name.clone(),
            )
            .decrement(1.0);
        }

        if let Some(upstream_start) = self.upstream_start {
            metrics::histogram!(
                "gateway_upstream_request_duration_seconds",
                "domain" => self.domain_name.clone(),
                "route" => self.route_name.clone(),
                "cluster" => self.cluster_name.clone(),
                "upstream" => self.upstream_addr.clone(),
            )
            .record(upstream_start.elapsed().as_secs_f64());
        }

        hyper::Response::builder()
            .status(status)
            .header("content-type", "application/json")
            .body(full_body(format!(r#"{{"error":"{}"}}"#, msg)))
            .unwrap()
    }

    pub fn finalize_metrics(&self, resp_status: u16) {
        let mut buf = itoa::Buffer::new();
        let status_str = buf.format(resp_status);

        metrics::counter!(
            "gateway_http_requests_total",
            "domain" => self.domain_name.clone(),
            "route" => self.route_name.clone(),
            "cluster" => self.cluster_name.clone(),
            "method" => self.method.clone(),
            "status_code" => status_str.to_owned(),
            "upstream" => self.upstream_addr.clone(),
        )
        .increment(1);

        metrics::histogram!(
            "gateway_http_request_duration_seconds",
            "domain" => self.domain_name.clone(),
            "route" => self.route_name.clone(),
            "cluster" => self.cluster_name.clone(),
            "upstream" => self.upstream_addr.clone(),
        )
        .record(self.start.elapsed().as_secs_f64());

        if let Some(upstream_start) = self.upstream_start {
            metrics::histogram!(
                "gateway_upstream_request_duration_seconds",
                "domain" => self.domain_name.clone(),
                "route" => self.route_name.clone(),
                "cluster" => self.cluster_name.clone(),
                "upstream" => self.upstream_addr.clone(),
            )
            .record(upstream_start.elapsed().as_secs_f64());
        }

        metrics::gauge!(
            "gateway_http_requests_in_flight",
            "domain" => self.domain_name.clone(),
            "route" => self.route_name.clone(),
        )
        .decrement(1.0);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{IpAddr, Ipv4Addr};

    #[test]
    fn test_full_body_creates_boxed_body() {
        let body = full_body("hello");
        let _ = body;
    }

    #[test]
    fn test_full_body_with_bytes() {
        let body = full_body(bytes::Bytes::from_static(b"data"));
        let _ = body;
    }

    #[test]
    fn test_empty_body() {
        let body = empty_body();
        let _ = body;
    }

    #[test]
    fn test_request_context_new() {
        let ctx = RequestContext::new(
            "example.com".to_string(),
            "/api/v1".to_string(),
            "GET".to_string(),
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
        );
        assert_eq!(ctx.host, "example.com");
        assert_eq!(ctx.uri_path, "/api/v1");
        assert_eq!(ctx.method, "GET");
        assert_eq!(ctx.client_ip, IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)));
        assert_eq!(ctx.domain_name, "");
        assert_eq!(ctx.route_name, "");
        assert_eq!(ctx.cluster_name, "");
        assert_eq!(ctx.upstream_addr, "");
        assert!(ctx.upstream_start.is_none());
        assert!(ctx.route.is_none());
    }

    #[test]
    fn test_request_context_error_response() {
        let ctx = RequestContext::new(
            "h.com".to_string(),
            "/".to_string(),
            "POST".to_string(),
            IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)),
        );

        let resp = ctx.error_response(http::StatusCode::BAD_GATEWAY, "upstream failed");
        assert_eq!(resp.status(), http::StatusCode::BAD_GATEWAY);
        assert_eq!(
            resp.headers().get("content-type").unwrap(),
            "application/json"
        );
    }

    #[test]
    fn test_request_context_error_response_various_status() {
        let ctx = RequestContext::new(
            "h.com".to_string(),
            "/".to_string(),
            "GET".to_string(),
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
        );

        for status in &[
            http::StatusCode::BAD_REQUEST,
            http::StatusCode::NOT_FOUND,
            http::StatusCode::TOO_MANY_REQUESTS,
            http::StatusCode::INTERNAL_SERVER_ERROR,
            http::StatusCode::SERVICE_UNAVAILABLE,
        ] {
            let resp = ctx.error_response(*status, "err");
            assert_eq!(resp.status(), *status);
        }
    }

    #[test]
    fn test_request_context_finalize_metrics() {
        let mut ctx = RequestContext::new(
            "h.com".to_string(),
            "/".to_string(),
            "GET".to_string(),
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
        );
        ctx.domain_name = "test-domain".to_string();
        ctx.route_name = "test-route".to_string();
        ctx.cluster_name = "test-cluster".to_string();
        ctx.upstream_addr = "10.0.0.1:8080".to_string();
        ctx.upstream_start = Some(std::time::Instant::now());

        ctx.finalize_metrics(200);
        ctx.finalize_metrics(502);
    }

    #[test]
    fn test_request_context_finalize_metrics_no_upstream_start() {
        let mut ctx = RequestContext::new(
            "h.com".to_string(),
            "/".to_string(),
            "GET".to_string(),
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
        );
        ctx.domain_name = "d".to_string();
        ctx.route_name = "r".to_string();
        ctx.finalize_metrics(200);
    }
}
