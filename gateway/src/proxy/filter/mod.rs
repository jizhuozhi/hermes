pub mod rate_limit;

use crate::config::RateLimitConfig;
use crate::config::RouteConfig;
use crate::proxy::context::{BoxBody, RequestContext};
use rate_limit::RateLimiter;
use std::sync::atomic::AtomicU32;
use std::sync::Arc;

pub enum FilterResult {
    Continue,
    Reject(hyper::Response<BoxBody>),
}

/// Enum-based filter — static dispatch, zero heap allocation.
/// Adding a new filter: add module, variant, match arms in on_request/on_response,
/// and construction logic in build_route_filters.
pub enum Filter {
    RateLimit {
        config: RateLimitConfig,
        limiter: Arc<RateLimiter>,
    },
}

impl std::fmt::Debug for Filter {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Filter::RateLimit { config, .. } => f
                .debug_struct("RateLimit")
                .field("mode", &config.mode)
                .finish(),
        }
    }
}

impl Filter {
    pub async fn on_request(&self, ctx: &mut RequestContext) -> FilterResult {
        match self {
            Filter::RateLimit { config, limiter } => {
                rate_limit::rate_limit_on_request(config, limiter, ctx).await
            }
        }
    }

    pub fn on_response(&self, _ctx: &RequestContext, _resp: &mut hyper::Response<BoxBody>) {
        match self {
            Filter::RateLimit { .. } => {}
        }
    }
}

/// Build the filter chain for a route (called once at config load / hot-reload).
pub fn build_route_filters(
    route: &RouteConfig,
    instance_count: Option<Arc<AtomicU32>>,
) -> Vec<Filter> {
    let mut filters = Vec::new();

    if let Some(ref rl) = route.rate_limit {
        let limiter = match instance_count {
            Some(ref ic) => RateLimiter::with_instance_count(ic.clone()),
            None => RateLimiter::new(),
        };
        let limiter = Arc::new(limiter);
        limiter.start_gc();
        filters.push(Filter::RateLimit {
            config: rl.clone(),
            limiter,
        });
    }

    filters
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{RateLimitConfig, RouteConfig, WeightedCluster};

    fn make_route(rate_limit: Option<RateLimitConfig>) -> RouteConfig {
        RouteConfig {
            id: "test".into(),
            name: "test-route".into(),
            uri: "/".into(),
            methods: vec![],
            headers: vec![],
            priority: 0,
            clusters: vec![WeightedCluster {
                name: "backend".into(),
                weight: 100,
            }],
            rate_limit,
            cluster_override_header: None,
            request_header_transforms: vec![],
            response_header_transforms: vec![],
            max_body_bytes: None,
            enable_compression: false,
            status: 1,
            plugins: None,
        }
    }

    #[test]
    fn test_filter_result_continue() {
        let result = FilterResult::Continue;
        match result {
            FilterResult::Continue => {}
            FilterResult::Reject(_) => panic!("expected Continue"),
        }
    }

    #[test]
    fn test_build_filters_no_rate_limit() {
        let route = make_route(None);
        let filters = build_route_filters(&route, None);
        assert!(filters.is_empty());
    }

    #[tokio::test]
    async fn test_build_filters_with_rate_limit() {
        let rl = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: Some(50),
            count: None,
            time_window: None,
            key: "host_uri".into(),
            rejected_code: 429,
        };
        let route = make_route(Some(rl));
        let filters = build_route_filters(&route, None);
        assert_eq!(filters.len(), 1);
        match &filters[0] {
            Filter::RateLimit { config, .. } => {
                assert_eq!(config.mode, "req");
                assert_eq!(config.rate, Some(100.0));
            }
        }
    }

    #[tokio::test]
    async fn test_build_filters_with_instance_count() {
        let rl = RateLimitConfig {
            mode: "count".into(),
            rate: None,
            burst: None,
            count: Some(1000),
            time_window: Some(60),
            key: "route".into(),
            rejected_code: 503,
        };
        let route = make_route(Some(rl));
        let instance_count = Arc::new(AtomicU32::new(3));
        let filters = build_route_filters(&route, Some(instance_count));
        assert_eq!(filters.len(), 1);
    }

    #[tokio::test]
    async fn test_filter_debug_format() {
        let rl = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: Some(50),
            count: None,
            time_window: None,
            key: "host_uri".into(),
            rejected_code: 429,
        };
        let route = make_route(Some(rl));
        let filters = build_route_filters(&route, None);
        let debug = format!("{:?}", filters[0]);
        assert!(debug.contains("RateLimit"));
        assert!(debug.contains("req"));
    }
}
