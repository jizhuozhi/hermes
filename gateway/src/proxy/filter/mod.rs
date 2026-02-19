pub mod rate_limit;

use crate::config::RateLimitConfig;
use crate::config::RouteConfig;
use crate::proxy::context::{BoxBody, RequestContext};
use rate_limit::RateLimiter;
use std::sync::atomic::AtomicU32;
use std::sync::Arc;

/// Result of a filter's on_request phase.
pub enum FilterResult {
    /// Continue to the next filter / phase.
    Continue,
    /// Short-circuit: return this response immediately.
    Reject(hyper::Response<BoxBody>),
}

/// Enum-based filter — static dispatch, exhaustive match, zero heap allocation.
///
/// Each variant holds the config/state it needs. Filters are pre-built once
/// when the route is compiled (at config load / hot-reload time), NOT per-request.
///
/// Adding a new filter:
/// 1. Add a module under `filter/`
/// 2. Add a variant here
/// 3. Implement the two match arms in `on_request` / `on_response`
/// 4. Add construction logic in `build_route_filters`
pub enum Filter {
    RateLimit {
        config: RateLimitConfig,
        /// Each route gets its own RateLimiter instance so buckets/windows
        /// are isolated per route — no cross-route interference.
        /// The instance_count inside is shared across all routes.
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
    /// Request phase — runs before upstream selection.
    /// Return `FilterResult::Reject` to short-circuit.
    pub async fn on_request(&self, ctx: &mut RequestContext) -> FilterResult {
        match self {
            Filter::RateLimit { config, limiter } => {
                rate_limit::rate_limit_on_request(config, limiter, ctx).await
            }
        }
    }

    /// Response phase — runs after upstream response, before sending to client.
    /// Can mutate the response (add headers, compress body, etc.).
    pub fn on_response(&self, _ctx: &RequestContext, _resp: &mut hyper::Response<BoxBody>) {
        match self {
            Filter::RateLimit { .. } => {
                // No response-phase logic for rate limiting.
            }
        }
    }
}

/// Build the filter chain for a route at compile time (config load / hot-reload).
/// This is called once per route, NOT per request.
///
/// `instance_count` is the shared atomic counter tracking the number of gateway
/// instances. When `Some`, distributed rate limiting is active and each limiter
/// divides the configured rate by the instance count.
///
/// Order matters:
/// 1. RateLimit  (reject early, save upstream resources)
///
/// Future:
/// 2. IpRestriction
/// 3. Cors
/// 4. Compression (response phase only)
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
