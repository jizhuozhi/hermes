use metrics::{describe_counter, describe_gauge, describe_histogram, Unit};
use metrics_exporter_prometheus::{PrometheusBuilder, PrometheusHandle};

/// Histogram bucket boundaries for latency metrics (seconds).
const LATENCY_BUCKETS: &[f64] = &[
    0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
];

/// Histogram bucket boundaries for response body size (bytes).
const SIZE_BUCKETS: &[f64] = &[
    100.0, 500.0, 1000.0, 5000.0, 10000.0, 50000.0, 100000.0, 500000.0, 1000000.0,
];

/// Thin handle around the global metrics recorder.
///
/// After `Metrics::install()` the `metrics` crate macros (`counter!`, `gauge!`,
/// `histogram!`) can be used anywhere in the codebase. The `PrometheusHandle`
/// is retained solely for rendering the `/metrics` endpoint.
#[derive(Clone)]
pub struct Metrics {
    handle: PrometheusHandle,
}

impl Metrics {
    /// Install the global Prometheus recorder and register metric descriptions.
    ///
    /// Must be called **once** at startup before any `counter!` / `gauge!` /
    /// `histogram!` calls.
    pub fn install() -> Self {
        let handle = PrometheusBuilder::new()
            .set_buckets_for_metric(
                metrics_exporter_prometheus::Matcher::Suffix("_duration_seconds".to_string()),
                LATENCY_BUCKETS,
            )
            .expect("valid matcher")
            .set_buckets_for_metric(
                metrics_exporter_prometheus::Matcher::Full("gateway_http_response_size_bytes".to_string()),
                SIZE_BUCKETS,
            )
            .expect("valid matcher")
            .install_recorder()
            .expect("failed to install metrics recorder");

        // --- Describe all metrics (adds HELP / TYPE lines) ---

        // request path
        describe_counter!(
            "gateway_http_requests_total",
            Unit::Count,
            "Total HTTP requests processed"
        );
        describe_histogram!(
            "gateway_http_request_duration_seconds",
            Unit::Seconds,
            "Total request duration from client perspective"
        );
        describe_histogram!(
            "gateway_upstream_request_duration_seconds",
            Unit::Seconds,
            "Upstream request duration (time spent waiting for upstream)"
        );
        describe_gauge!(
            "gateway_http_requests_in_flight",
            Unit::Count,
            "Number of requests currently being processed"
        );
        describe_histogram!(
            "gateway_http_response_size_bytes",
            Unit::Bytes,
            "Response body size in bytes"
        );

        // rate limiting
        describe_counter!(
            "gateway_rate_limit_rejected_total",
            Unit::Count,
            "Total requests rejected by rate limiter"
        );
        describe_counter!(
            "gateway_rate_limit_allowed_total",
            Unit::Count,
            "Total requests allowed by rate limiter"
        );

        // upstream health
        describe_gauge!(
            "gateway_upstream_health_status",
            Unit::Count,
            "Upstream node health: 1=healthy 0=unhealthy"
        );
        describe_counter!(
            "gateway_health_check_total",
            Unit::Count,
            "Total active health check attempts"
        );

        // service discovery
        describe_gauge!(
            "gateway_consul_discovered_nodes",
            Unit::Count,
            "Number of nodes discovered from consul per service"
        );
        describe_counter!(
            "gateway_consul_poll_total",
            Unit::Count,
            "Total consul poll attempts"
        );

        // connections
        describe_gauge!(
            "gateway_connections_active",
            Unit::Count,
            "Number of active downstream connections"
        );
        describe_counter!(
            "gateway_connections_total",
            Unit::Count,
            "Total connections accepted"
        );

        // config
        describe_gauge!(
            "gateway_config_routes_total",
            Unit::Count,
            "Number of routes currently loaded"
        );
        describe_counter!(
            "gateway_config_reloads_total",
            Unit::Count,
            "Config reload events"
        );

        // retries & circuit breaker
        describe_counter!(
            "gateway_upstream_retries_total",
            Unit::Count,
            "Total upstream retry attempts"
        );
        describe_counter!(
            "gateway_circuit_breaker_rejected_total",
            Unit::Count,
            "Total requests rejected by circuit breaker"
        );
        describe_counter!(
            "gateway_cluster_override_total",
            Unit::Count,
            "Total requests where cluster selection was overridden via header"
        );

        Self { handle }
    }

    /// Render all metrics in Prometheus text exposition format.
    pub fn render(&self) -> String {
        self.handle.render()
    }
}
