use crate::config::RateLimitConfig;
use crate::proxy::context::RequestContext;
use dashmap::DashMap;
use http::StatusCode;
use std::borrow::Cow;
use std::sync::atomic::{AtomicU32, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::Mutex;

use super::FilterResult;

/// High-performance rate limiter using atomic operations instead of mutexes.
///
/// Two modes:
/// - Token bucket (mode "req"): smooth rate limiting with burst.
/// - Sliding window (mode "count"): count-based limiting per time window,
///   with previous-window blending to prevent boundary burst.
///
/// Supports distributed rate limiting: when `instance_count > 1`, the configured
/// rate/count is divided evenly across instances. Each instance enforces
/// `rate / N` locally, achieving approximate global rate limiting without
/// cross-instance coordination.
///
/// **GC**: Entries that have not been accessed for `GC_EXPIRE_SECS` are
/// periodically evicted to prevent unbounded growth under dynamic-path workloads
/// (e.g. `/api/users/:id` where each user ID creates a unique key).
pub struct RateLimiter {
    buckets: DashMap<String, Arc<Bucket>>,
    windows: DashMap<String, Arc<SlidingWindow>>,
    /// Total number of gateway instances. Updated dynamically via etcd watch.
    /// 1 means standalone mode (no division). Always >= 1.
    instance_count: Arc<AtomicU32>,
}

/// Token bucket — `tokio::sync::Mutex`-protected for correctness under high concurrency.
///
/// The previous CAS-based design had a subtle bug: only the thread that won
/// the `last_refill` CAS would add tokens; all other concurrent callers
/// skipped the refill entirely, causing the effective refill rate to drop
/// well below the configured rate under contention.
///
/// `tokio::sync::Mutex` is used so that waiting for the lock yields back to
/// the tokio runtime instead of blocking the worker thread.
struct Bucket {
    inner: Mutex<BucketInner>,
    /// Last access timestamp in microseconds (for GC). Atomic — updated outside the lock.
    last_access: AtomicU64,
}

struct BucketInner {
    tokens: u64,
    last_refill: u64,
    rate_per_us: f64,
    max_tokens: u64,
}

/// Sliding window counter — approximates a true sliding window by blending
/// the previous window's count with the current window's count based on
/// how far we are into the current window.
///
/// This eliminates the classic fixed-window edge-case where two adjacent
/// windows can collectively pass 2× the configured limit in a short burst
/// around the boundary.
///
/// Algorithm (sliding window log approximation):
///   estimated_count = prev_count × (1 - elapsed_ratio) + current_count
///   where elapsed_ratio = time_into_current_window / window_duration
///
/// Reference: Cloudflare, Redis, Envoy all use this approach.
struct SlidingWindow {
    inner: Mutex<SlidingWindowInner>,
    last_access: AtomicU64,
}

struct SlidingWindowInner {
    /// Count in the current window.
    current_count: u64,
    /// Count from the previous window (used for blending).
    prev_count: u64,
    /// Start time of the current window in microseconds.
    window_start: u64,
    /// Maximum allowed requests per window.
    max_count: u64,
    /// Window duration in microseconds.
    window_us: u64,
}

/// Entries not accessed for this many seconds are eligible for eviction.
const GC_EXPIRE_SECS: u64 = 300; // 5 minutes
/// GC runs every this many seconds.
const GC_INTERVAL_SECS: u64 = 60;
/// Hard cap on entries per DashMap. When exceeded, the oldest entries beyond
/// this limit are force-evicted regardless of last-access time.
const MAX_ENTRIES: usize = 100_000;

impl Default for RateLimiter {
    fn default() -> Self {
        Self {
            buckets: DashMap::new(),
            windows: DashMap::new(),
            instance_count: Arc::new(AtomicU32::new(1)),
        }
    }
}

impl RateLimiter {
    pub fn new() -> Self {
        Self::default()
    }

    /// Create a rate limiter with a shared instance count (for distributed mode).
    pub fn with_instance_count(instance_count: Arc<AtomicU32>) -> Self {
        Self {
            buckets: DashMap::new(),
            windows: DashMap::new(),
            instance_count,
        }
    }

    /// Get the shared instance count handle (for the instance registry to update).
    pub fn instance_count_handle(&self) -> Arc<AtomicU32> {
        self.instance_count.clone()
    }

    fn current_instance_count(&self) -> u32 {
        self.instance_count.load(Ordering::Relaxed).max(1)
    }

    /// Returns `true` if allowed, `false` if rate limited.
    pub async fn check(&self, config: &RateLimitConfig, key: &str) -> bool {
        match config.mode.as_str() {
            "count" => self.check_sliding_window(config, key).await,
            _ => self.check_token_bucket(config, key).await,
        }
    }

    async fn check_token_bucket(&self, config: &RateLimitConfig, key: &str) -> bool {
        let n = self.current_instance_count() as f64;
        let rate = config.rate.unwrap_or(100.0) / n;
        let burst = (config.burst.unwrap_or(rate as u64)).max(1);
        let max_tokens = (rate as u64 + burst) * PRECISION;
        let rate_per_us = rate / 1_000_000.0;

        // Fast path: key already exists — no allocation.
        let bucket = if let Some(entry) = self.buckets.get(key) {
            entry.value().clone()
        } else {
            self.buckets
                .entry(key.to_string())
                .or_insert_with(|| {
                    let now = now_us();
                    Arc::new(Bucket {
                        inner: Mutex::new(BucketInner {
                            tokens: max_tokens,
                            last_refill: now,
                            rate_per_us,
                            max_tokens,
                        }),
                        last_access: AtomicU64::new(now),
                    })
                })
                .clone()
        };

        bucket.last_access.store(now_us(), Ordering::Relaxed);
        bucket.try_acquire().await
    }

    async fn check_sliding_window(&self, config: &RateLimitConfig, key: &str) -> bool {
        let n = self.current_instance_count() as u64;
        let max_count = (config.count.unwrap_or(1000) / n).max(1);
        let window_secs = config.time_window.unwrap_or(1);

        // Fast path: key already exists — no allocation.
        let window = if let Some(entry) = self.windows.get(key) {
            entry.value().clone()
        } else {
            self.windows
                .entry(key.to_string())
                .or_insert_with(|| {
                    let now = now_us();
                    Arc::new(SlidingWindow {
                        inner: Mutex::new(SlidingWindowInner {
                            current_count: 0,
                            prev_count: 0,
                            window_start: now,
                            max_count,
                            window_us: window_secs * 1_000_000,
                        }),
                        last_access: AtomicU64::new(now),
                    })
                })
                .clone()
        };

        window.last_access.store(now_us(), Ordering::Relaxed);
        window.try_acquire().await
    }

    /// Extract the rate limit key from a request.
    ///
    /// Supported key modes:
    /// - `"route"`:       all requests hitting the same route share one counter (recommended)
    /// - `"remote_addr"`: per-client-IP counter (uses the real client IP from RequestContext)
    /// - `"uri"`:         per-URI path (caution: dynamic paths cause unbounded keys)
    /// - `"host_uri"` (default): per host+URI combination
    ///
    /// Returns `Cow::Borrowed` when possible (route / uri modes)
    /// to avoid heap allocation on the hot path.
    pub fn extract_key<'a>(
        config: &RateLimitConfig,
        route_name: &'a str,
        host: &'a str,
        uri: &'a str,
        client_ip: &std::net::IpAddr,
    ) -> Cow<'a, str> {
        match config.key.as_str() {
            "route" => Cow::Borrowed(route_name),
            "remote_addr" => Cow::Owned(client_ip.to_string()),
            "uri" => Cow::Borrowed(uri),
            _ => {
                let mut s = String::with_capacity(host.len() + uri.len());
                s.push_str(host);
                s.push_str(uri);
                Cow::Owned(s)
            }
        }
    }

    /// Spawn a background tokio task that periodically evicts stale entries.
    /// Call this once after constructing the limiter.
    pub fn start_gc(self: &Arc<Self>) {
        let limiter = Arc::clone(self);
        tokio::spawn(async move {
            let mut interval =
                tokio::time::interval(std::time::Duration::from_secs(GC_INTERVAL_SECS));
            loop {
                interval.tick().await;
                limiter.evict_stale();
            }
        });
    }

    /// Remove entries that have not been accessed for `GC_EXPIRE_SECS`.
    /// If either map still exceeds `MAX_ENTRIES` after time-based eviction,
    /// force-evict the oldest entries until it is under the cap.
    fn evict_stale(&self) {
        let now = now_us();
        let expire_us = GC_EXPIRE_SECS * 1_000_000;

        // --- buckets ---
        self.buckets
            .retain(|_, v| now.saturating_sub(v.last_access.load(Ordering::Relaxed)) < expire_us);

        if self.buckets.len() > MAX_ENTRIES {
            self.force_evict_buckets(now);
        }

        // --- windows ---
        self.windows
            .retain(|_, v| now.saturating_sub(v.last_access.load(Ordering::Relaxed)) < expire_us);

        if self.windows.len() > MAX_ENTRIES {
            self.force_evict_windows(now);
        }
    }

    /// Force-evict oldest bucket entries until we are at or below `MAX_ENTRIES`.
    fn force_evict_buckets(&self, now: u64) {
        let overflow = self.buckets.len().saturating_sub(MAX_ENTRIES);
        if overflow == 0 {
            return;
        }
        // Collect (key, age) pairs, sort by age descending, remove the oldest.
        let mut entries: Vec<(String, u64)> = self
            .buckets
            .iter()
            .map(|r| {
                let age = now.saturating_sub(r.value().last_access.load(Ordering::Relaxed));
                (r.key().clone(), age)
            })
            .collect();
        entries.sort_unstable_by(|a, b| b.1.cmp(&a.1));
        for (key, _) in entries.into_iter().take(overflow) {
            self.buckets.remove(&key);
        }
    }

    /// Force-evict oldest window entries until we are at or below `MAX_ENTRIES`.
    fn force_evict_windows(&self, now: u64) {
        let overflow = self.windows.len().saturating_sub(MAX_ENTRIES);
        if overflow == 0 {
            return;
        }
        let mut entries: Vec<(String, u64)> = self
            .windows
            .iter()
            .map(|r| {
                let age = now.saturating_sub(r.value().last_access.load(Ordering::Relaxed));
                (r.key().clone(), age)
            })
            .collect();
        entries.sort_unstable_by(|a, b| b.1.cmp(&a.1));
        for (key, _) in entries.into_iter().take(overflow) {
            self.windows.remove(&key);
        }
    }
}

const PRECISION: u64 = 1_000_000;

fn now_us() -> u64 {
    static START: std::sync::OnceLock<Instant> = std::sync::OnceLock::new();
    let start = START.get_or_init(Instant::now);
    start.elapsed().as_micros() as u64
}

impl Bucket {
    async fn try_acquire(&self) -> bool {
        let now = now_us();
        let mut b = self.inner.lock().await;

        // Refill tokens based on elapsed time.
        let elapsed = now.saturating_sub(b.last_refill);
        if elapsed > 0 {
            let refill = (elapsed as f64 * b.rate_per_us * PRECISION as f64) as u64;
            b.tokens = (b.tokens + refill).min(b.max_tokens);
            b.last_refill = now;
        }

        // Try to consume one token.
        let cost = PRECISION;
        if b.tokens >= cost {
            b.tokens -= cost;
            true
        } else {
            false
        }
    }
}

impl SlidingWindow {
    async fn try_acquire(&self) -> bool {
        let now = now_us();
        let mut w = self.inner.lock().await;

        // Advance windows if the current window has expired.
        // May need to advance more than once if a long pause occurred.
        while now.saturating_sub(w.window_start) >= w.window_us {
            w.prev_count = w.current_count;
            w.current_count = 0;
            w.window_start += w.window_us;
        }

        // If we've been idle for 2+ full windows, prev_count should be 0.
        // The while-loop above handles single-window advancement; check for
        // the case where window_start is still behind.
        if now.saturating_sub(w.window_start) >= w.window_us {
            w.prev_count = 0;
        }

        // Sliding window estimate:
        //   elapsed_ratio = how far into the current window we are (0.0 .. 1.0)
        //   weight        = portion of prev window that still "overlaps"
        //   estimated     = prev_count * weight + current_count
        let elapsed_in_window = now.saturating_sub(w.window_start);
        let weight = if w.window_us > 0 {
            1.0 - (elapsed_in_window as f64 / w.window_us as f64)
        } else {
            0.0
        };
        let estimated = (w.prev_count as f64 * weight) as u64 + w.current_count;

        if estimated < w.max_count {
            w.current_count += 1;
            true
        } else {
            false
        }
    }
}

pub(super) async fn rate_limit_on_request(
    config: &RateLimitConfig,
    limiter: &RateLimiter,
    ctx: &mut RequestContext,
) -> FilterResult {
    let key = RateLimiter::extract_key(
        config,
        &ctx.route_name,
        &ctx.host,
        &ctx.uri_path,
        &ctx.client_ip,
    );

    if !limiter.check(config, &key).await {
        let rejected_code = config.rejected_code;
        let status = StatusCode::from_u16(rejected_code).unwrap_or(StatusCode::TOO_MANY_REQUESTS);

        tracing::debug!(
            "filter: rate_limit: rejected, route={}, key={}",
            ctx.route_name,
            key
        );

        metrics::counter!(
            "gateway_rate_limit_rejected_total",
            "domain" => ctx.domain_name.clone(),
            "route" => ctx.route_name.clone(),
            "mode" => config.mode.clone(),
        )
        .increment(1);

        return FilterResult::Reject(ctx.error_response(status, "too many requests"));
    }

    metrics::counter!(
        "gateway_rate_limit_allowed_total",
        "domain" => ctx.domain_name.clone(),
        "route" => ctx.route_name.clone(),
        "mode" => config.mode.clone(),
    )
    .increment(1);

    FilterResult::Continue
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::RateLimitConfig;
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;

    fn token_bucket_config(rate: f64, burst: u64) -> RateLimitConfig {
        RateLimitConfig {
            mode: "req".to_string(),
            rate: Some(rate),
            burst: Some(burst),
            count: None,
            time_window: None,
            key: "route".to_string(),
            rejected_code: 429,
        }
    }

    fn sliding_window_config(count: u64, window_secs: u64) -> RateLimitConfig {
        RateLimitConfig {
            mode: "count".to_string(),
            rate: None,
            burst: None,
            count: Some(count),
            time_window: Some(window_secs),
            key: "route".to_string(),
            rejected_code: 429,
        }
    }

    #[tokio::test]
    async fn test_token_bucket_allows_burst() {
        let limiter = RateLimiter::new();
        let config = token_bucket_config(10.0, 10);

        let mut allowed = 0;
        for _ in 0..20 {
            if limiter.check(&config, "test-key").await {
                allowed += 1;
            }
        }
        assert!(
            allowed >= 10,
            "expected at least 10 allowed, got {}",
            allowed
        );
    }

    #[tokio::test]
    async fn test_token_bucket_rejects_after_burst() {
        let limiter = RateLimiter::new();
        let config = token_bucket_config(1.0, 1);

        let mut allowed = 0;
        for _ in 0..100 {
            if limiter.check(&config, "exhaust-key").await {
                allowed += 1;
            }
        }
        assert!(
            allowed < 50,
            "expected most requests rejected, got {} allowed",
            allowed
        );
    }

    #[tokio::test]
    async fn test_sliding_window_basic() {
        let limiter = RateLimiter::new();
        let config = sliding_window_config(5, 60);

        for i in 0..5 {
            assert!(
                limiter.check(&config, "window-key").await,
                "request {} should be allowed",
                i
            );
        }
        assert!(
            !limiter.check(&config, "window-key").await,
            "request 6 should be rejected"
        );
    }

    #[tokio::test]
    async fn test_different_keys_independent() {
        let limiter = RateLimiter::new();
        let config = sliding_window_config(2, 60);

        assert!(limiter.check(&config, "key-a").await);
        assert!(limiter.check(&config, "key-a").await);
        assert!(!limiter.check(&config, "key-a").await);

        assert!(limiter.check(&config, "key-b").await);
        assert!(limiter.check(&config, "key-b").await);
        assert!(!limiter.check(&config, "key-b").await);
    }

    #[tokio::test]
    async fn test_distributed_mode_divides_limit() {
        let instance_count = Arc::new(AtomicU32::new(2));
        let limiter = RateLimiter::with_instance_count(instance_count.clone());
        let config = sliding_window_config(10, 60);

        let mut allowed = 0;
        for _ in 0..10 {
            if limiter.check(&config, "dist-key").await {
                allowed += 1;
            }
        }
        assert_eq!(allowed, 5, "with 2 instances, should allow 5 out of 10");
    }

    #[tokio::test]
    async fn test_distributed_mode_dynamic_update() {
        let instance_count = Arc::new(AtomicU32::new(1));
        let limiter = RateLimiter::with_instance_count(instance_count.clone());
        let config = sliding_window_config(10, 60);

        let mut allowed = 0;
        for _ in 0..10 {
            if limiter.check(&config, "dyn-key").await {
                allowed += 1;
            }
        }
        assert_eq!(allowed, 10);

        instance_count.store(5, Ordering::Relaxed);
        let mut allowed = 0;
        for _ in 0..5 {
            if limiter.check(&config, "dyn-key-2").await {
                allowed += 1;
            }
        }
        assert_eq!(allowed, 2, "with 5 instances, should allow 2 out of 5");
    }

    #[tokio::test]
    async fn test_extract_key_modes() {
        let test_ip: std::net::IpAddr = "10.0.0.1".parse().unwrap();

        let config_route = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: None,
            count: None,
            time_window: None,
            key: "route".into(),
            rejected_code: 429,
        };
        assert_eq!(
            RateLimiter::extract_key(&config_route, "my-route", "example.com", "/api/v1", &test_ip)
                .as_ref(),
            "my-route"
        );

        let config_remote = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: None,
            count: None,
            time_window: None,
            key: "remote_addr".into(),
            rejected_code: 429,
        };
        assert_eq!(
            RateLimiter::extract_key(&config_remote, "r", "example.com", "/api", &test_ip)
                .as_ref(),
            "10.0.0.1"
        );

        let config_uri = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: None,
            count: None,
            time_window: None,
            key: "uri".into(),
            rejected_code: 429,
        };
        assert_eq!(
            RateLimiter::extract_key(&config_uri, "r", "example.com", "/api/v1", &test_ip)
                .as_ref(),
            "/api/v1"
        );

        let config_host_uri = RateLimitConfig {
            mode: "req".into(),
            rate: Some(100.0),
            burst: None,
            count: None,
            time_window: None,
            key: "host_uri".into(),
            rejected_code: 429,
        };
        assert_eq!(
            RateLimiter::extract_key(&config_host_uri, "r", "example.com", "/api", &test_ip)
                .as_ref(),
            "example.com/api"
        );
    }

    #[tokio::test]
    async fn test_instance_count_handle() {
        let limiter = RateLimiter::new();
        let handle = limiter.instance_count_handle();
        assert_eq!(handle.load(Ordering::Relaxed), 1);
        handle.store(3, Ordering::Relaxed);
        assert_eq!(handle.load(Ordering::Relaxed), 3);
    }
}
