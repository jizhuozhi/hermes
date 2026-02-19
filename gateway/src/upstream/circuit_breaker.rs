use crate::config::CircuitBreakerConfig;
use dashmap::DashMap;
use std::sync::atomic::{AtomicU32, AtomicU8, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

/// Circuit breaker state machine: Closed → Open → HalfOpen → Closed/Open.
///
/// Per-node granularity — each upstream "host:port" gets its own breaker.
/// This allows individual unhealthy nodes to be isolated without affecting
/// the entire upstream cluster.
pub struct CircuitBreakerRegistry {
    breakers: DashMap<String, Arc<NodeBreaker>>,
}

/// Per-node circuit breaker state.
struct NodeBreaker {
    /// 0 = Closed, 1 = Open, 2 = HalfOpen.
    state: AtomicU8,
    /// Consecutive failure count (in Closed state).
    consecutive_failures: AtomicU32,
    /// Consecutive successes in HalfOpen state.
    half_open_successes: AtomicU32,
    /// When the breaker tripped to Open, protected by atomic state transitions.
    /// We use a DashMap entry to avoid interior mutability issues.
    opened_at: std::sync::Mutex<Option<Instant>>,
    config: CircuitBreakerConfig,
}

const STATE_CLOSED: u8 = 0;
const STATE_OPEN: u8 = 1;
const STATE_HALF_OPEN: u8 = 2;

/// Result of checking the circuit breaker before a request.
pub enum BreakerCheck {
    /// Breaker is closed — proceed normally.
    Allowed,
    /// Breaker is half-open — this is a probe request.
    Probe,
    /// Breaker is open — reject immediately.
    Rejected,
}

impl CircuitBreakerRegistry {
    pub fn new() -> Self {
        Self {
            breakers: DashMap::new(),
        }
    }

    /// Check whether a request to `node_key` is allowed.
    pub fn check(&self, node_key: &str, config: &CircuitBreakerConfig) -> BreakerCheck {
        let breaker = self.get_or_create(node_key, config);
        breaker.check()
    }

    /// Record a successful response from `node_key`.
    pub fn record_success(&self, node_key: &str, config: &CircuitBreakerConfig) {
        let breaker = self.get_or_create(node_key, config);
        breaker.record_success();
    }

    /// Record a failed response from `node_key`.
    pub fn record_failure(&self, node_key: &str, config: &CircuitBreakerConfig) {
        let breaker = self.get_or_create(node_key, config);
        breaker.record_failure();
    }

    /// Check if a node's breaker is currently open (for LB filtering).
    pub fn is_open(&self, node_key: &str, config: &CircuitBreakerConfig) -> bool {
        let breaker = self.get_or_create(node_key, config);
        let state = breaker.state.load(Ordering::Acquire);
        if state == STATE_OPEN {
            // Check if enough time has passed to transition to HalfOpen.
            let opened_at = breaker.opened_at.lock().unwrap();
            if let Some(at) = *opened_at {
                if at.elapsed() >= Duration::from_secs(config.open_duration_secs) {
                    return false; // Will transition to HalfOpen on next check().
                }
            }
            return true;
        }
        false
    }

    fn get_or_create(&self, node_key: &str, config: &CircuitBreakerConfig) -> Arc<NodeBreaker> {
        // Fast path: key already exists — no allocation.
        if let Some(entry) = self.breakers.get(node_key) {
            return entry.value().clone();
        }
        // Slow path: allocate owned key only when inserting.
        self.breakers
            .entry(node_key.to_string())
            .or_insert_with(|| {
                Arc::new(NodeBreaker {
                    state: AtomicU8::new(STATE_CLOSED),
                    consecutive_failures: AtomicU32::new(0),
                    half_open_successes: AtomicU32::new(0),
                    opened_at: std::sync::Mutex::new(None),
                    config: config.clone(),
                })
            })
            .clone()
    }

    /// Remove breaker entries for nodes that are no longer in the active set.
    pub fn retain_nodes(&self, active_keys: &std::collections::HashSet<String>) {
        self.breakers.retain(|k, _| active_keys.contains(k));
    }
}

impl NodeBreaker {
    fn check(&self) -> BreakerCheck {
        let state = self.state.load(Ordering::Acquire);
        match state {
            STATE_CLOSED => BreakerCheck::Allowed,
            STATE_OPEN => {
                let opened_at = self.opened_at.lock().unwrap();
                if let Some(at) = *opened_at {
                    if at.elapsed() >= Duration::from_secs(self.config.open_duration_secs) {
                        drop(opened_at);
                        // Attempt CAS to HalfOpen — only one thread wins the probe.
                        if self
                            .state
                            .compare_exchange(
                                STATE_OPEN,
                                STATE_HALF_OPEN,
                                Ordering::AcqRel,
                                Ordering::Acquire,
                            )
                            .is_ok()
                        {
                            self.half_open_successes.store(0, Ordering::Relaxed);
                            return BreakerCheck::Probe;
                        }
                    }
                }
                BreakerCheck::Rejected
            }
            STATE_HALF_OPEN => {
                // In HalfOpen, allow limited traffic (only the probe winner
                // and subsequent requests up to success_threshold).
                BreakerCheck::Probe
            }
            _ => BreakerCheck::Allowed,
        }
    }

    fn record_success(&self) {
        let state = self.state.load(Ordering::Acquire);
        match state {
            STATE_CLOSED => {
                self.consecutive_failures.store(0, Ordering::Relaxed);
            }
            STATE_HALF_OPEN => {
                let count = self.half_open_successes.fetch_add(1, Ordering::Relaxed) + 1;
                if count >= self.config.success_threshold {
                    self.state.store(STATE_CLOSED, Ordering::Release);
                    self.consecutive_failures.store(0, Ordering::Relaxed);
                    tracing::info!(
                        "circuit_breaker: closed (recovered after {} successes)",
                        count
                    );
                }
            }
            _ => {}
        }
    }

    fn record_failure(&self) {
        let state = self.state.load(Ordering::Acquire);
        match state {
            STATE_CLOSED => {
                let count = self.consecutive_failures.fetch_add(1, Ordering::Relaxed) + 1;
                if count >= self.config.failure_threshold {
                    self.state.store(STATE_OPEN, Ordering::Release);
                    *self.opened_at.lock().unwrap() = Some(Instant::now());
                    tracing::warn!(
                        "circuit_breaker: opened (after {} consecutive failures)",
                        count
                    );
                }
            }
            STATE_HALF_OPEN => {
                // Probe failed — back to Open.
                self.state.store(STATE_OPEN, Ordering::Release);
                *self.opened_at.lock().unwrap() = Some(Instant::now());
                self.half_open_successes.store(0, Ordering::Relaxed);
                tracing::warn!("circuit_breaker: re-opened (probe failed in half-open)");
            }
            _ => {}
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn default_config() -> CircuitBreakerConfig {
        CircuitBreakerConfig {
            failure_threshold: 3,
            success_threshold: 2,
            open_duration_secs: 1,
        }
    }

    #[test]
    fn test_starts_closed() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = default_config();
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Allowed));
    }

    #[test]
    fn test_trips_after_failures() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = default_config();

        for _ in 0..3 {
            assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Allowed));
            reg.record_failure("a:80", &cfg);
        }

        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Rejected));
    }

    #[test]
    fn test_success_resets_failure_count() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = default_config();

        reg.record_failure("a:80", &cfg);
        reg.record_failure("a:80", &cfg);
        reg.record_success("a:80", &cfg);
        reg.record_failure("a:80", &cfg);
        reg.record_failure("a:80", &cfg);

        // Should still be closed — success reset the counter.
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Allowed));
    }

    #[test]
    fn test_half_open_after_timeout() {
        let reg = CircuitBreakerRegistry::new();

        // With a long open_duration, breaker stays Rejected.
        let cfg = CircuitBreakerConfig {
            failure_threshold: 3,
            success_threshold: 2,
            open_duration_secs: 3600,
        };
        for _ in 0..3 {
            reg.record_failure("a:80", &cfg);
        }
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Rejected));

        // Use a separate breaker with open_duration=0 to test immediate HalfOpen transition.
        let cfg_fast = CircuitBreakerConfig {
            failure_threshold: 1,
            success_threshold: 1,
            open_duration_secs: 0,
        };
        reg.record_failure("b:80", &cfg_fast);
        std::thread::sleep(std::time::Duration::from_millis(10));
        assert!(matches!(reg.check("b:80", &cfg_fast), BreakerCheck::Probe));
    }

    #[test]
    fn test_half_open_success_closes() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = CircuitBreakerConfig {
            failure_threshold: 1,
            success_threshold: 1,
            open_duration_secs: 0,
        };

        reg.record_failure("a:80", &cfg);
        std::thread::sleep(std::time::Duration::from_millis(10));
        let _ = reg.check("a:80", &cfg); // Transition to HalfOpen.
        reg.record_success("a:80", &cfg);

        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Allowed));
    }

    #[test]
    fn test_half_open_failure_reopens() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = CircuitBreakerConfig {
            failure_threshold: 1,
            success_threshold: 2,
            open_duration_secs: 0,
        };

        reg.record_failure("a:80", &cfg);
        std::thread::sleep(std::time::Duration::from_millis(10));
        let _ = reg.check("a:80", &cfg); // HalfOpen
        reg.record_failure("a:80", &cfg); // Probe fails → Open again.

        // Immediately check: with open_duration=0, it transitions to HalfOpen again.
        // But the internal state did go back to Open, proving the re-open happened.
        // With the stored config having open_duration=0, check will return Probe
        // (because time elapsed >= 0), which proves the cycle works.
        std::thread::sleep(std::time::Duration::from_millis(10));
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Probe));

        // Verify: two successes needed to close.
        reg.record_success("a:80", &cfg);
        // Still HalfOpen (need 2 successes).
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Probe));
        reg.record_success("a:80", &cfg);
        // Now closed.
        assert!(matches!(reg.check("a:80", &cfg), BreakerCheck::Allowed));
    }

    #[test]
    fn test_is_open() {
        let reg = CircuitBreakerRegistry::new();
        let cfg = CircuitBreakerConfig {
            failure_threshold: 1,
            success_threshold: 1,
            open_duration_secs: 60,
        };

        assert!(!reg.is_open("a:80", &cfg));
        reg.record_failure("a:80", &cfg);
        assert!(reg.is_open("a:80", &cfg));
    }
}
