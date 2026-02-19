use super::RateLimiter;
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

    // Should allow at least a burst of requests
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

    // Exhaust tokens
    let mut allowed = 0;
    for _ in 0..100 {
        if limiter.check(&config, "exhaust-key").await {
            allowed += 1;
        }
    }
    // Should have rejected most requests
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

    // First 5 should be allowed
    for i in 0..5 {
        assert!(
            limiter.check(&config, "window-key").await,
            "request {} should be allowed",
            i
        );
    }
    // 6th should be rejected
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

    // key-b should still be allowed
    assert!(limiter.check(&config, "key-b").await);
    assert!(limiter.check(&config, "key-b").await);
    assert!(!limiter.check(&config, "key-b").await);
}

#[tokio::test]
async fn test_distributed_mode_divides_limit() {
    let instance_count = Arc::new(AtomicU32::new(2));
    let limiter = RateLimiter::with_instance_count(instance_count.clone());
    let config = sliding_window_config(10, 60);

    // With 2 instances, effective limit per instance = 10/2 = 5
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

    // Initially 1 instance: 10 requests allowed
    let mut allowed = 0;
    for _ in 0..10 {
        if limiter.check(&config, "dyn-key").await {
            allowed += 1;
        }
    }
    assert_eq!(allowed, 10);

    // Simulate scaling to 5 instances — new keys should get 10/5=2
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
        RateLimiter::extract_key(
            &config_route,
            "my-route",
            "example.com",
            "/api/v1",
            &test_ip
        )
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
        RateLimiter::extract_key(&config_remote, "r", "example.com", "/api", &test_ip).as_ref(),
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
        RateLimiter::extract_key(&config_uri, "r", "example.com", "/api/v1", &test_ip).as_ref(),
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
        RateLimiter::extract_key(&config_host_uri, "r", "example.com", "/api", &test_ip).as_ref(),
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
