pub mod least_request;
pub mod peak_ewma;
pub mod random;
pub mod round_robin;

use crate::config::UpstreamNode;
use least_request::LeastRequestBalancer;
use peak_ewma::{LatencyGuard, PeakEwmaBalancer};
use random::RandomBalancer;
use round_robin::RoundRobinBalancer;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

/// A resolved upstream node carrying per-request state (active count, etc.).
/// Cheap to clone — the counters are shared via `Arc`.
#[derive(Debug, Clone)]
pub struct UpstreamInstance {
    pub host: String,
    pub port: u16,
    pub weight: u32,
    pub metadata: std::collections::HashMap<String, String>,
    pub active_requests: Arc<AtomicUsize>,
    /// Pre-computed "host:port" string — avoids a `format!()` allocation on
    /// every request in `endpoint()`, `select_healthy_node`, circuit breaker
    /// lookups, health recording, etc.
    endpoint: Arc<str>,
}

impl UpstreamInstance {
    /// Returns the cached "host:port" string. Zero allocation.
    #[inline]
    pub fn endpoint(&self) -> &str {
        &self.endpoint
    }

    pub fn inc_active(&self) {
        self.active_requests.fetch_add(1, Ordering::Relaxed);
    }

    pub fn dec_active(&self) {
        self.active_requests.fetch_sub(1, Ordering::Relaxed);
    }
}

impl From<&UpstreamNode> for UpstreamInstance {
    fn from(node: &UpstreamNode) -> Self {
        let endpoint: Arc<str> = format!("{}:{}", node.host, node.port).into();
        Self {
            host: node.host.clone(),
            port: node.port,
            weight: node.weight,
            metadata: node.metadata.clone(),
            active_requests: Arc::new(AtomicUsize::new(0)),
            endpoint,
        }
    }
}

/// Enum-based load balancer — no trait objects, no dynamic dispatch.
pub enum LoadBalancer {
    RoundRobin(RoundRobinBalancer),
    Random(RandomBalancer),
    LeastRequest(LeastRequestBalancer),
    PeakEwma(PeakEwmaBalancer),
}

impl LoadBalancer {
    pub fn new(lb_type: &str) -> Arc<Self> {
        match lb_type {
            "random" | "weighted_random" => Arc::new(Self::Random(RandomBalancer::new())),
            "least_request" | "least_conn" => {
                Arc::new(Self::LeastRequest(LeastRequestBalancer::new()))
            }
            "peak_ewma" | "ewma" => Arc::new(Self::PeakEwma(PeakEwmaBalancer::new_default())),
            _ => Arc::new(Self::RoundRobin(RoundRobinBalancer::new())),
        }
    }

    /// Atomically replace the instance list, reusing existing counters for
    /// instances that were already present.
    pub fn update_instances(&self, nodes: &[UpstreamNode]) {
        let instances: Vec<UpstreamInstance> = nodes.iter().map(UpstreamInstance::from).collect();
        match self {
            Self::RoundRobin(lb) => lb.update_instances(instances),
            Self::Random(lb) => lb.update_instances(instances),
            Self::LeastRequest(lb) => lb.update_instances(instances),
            Self::PeakEwma(lb) => lb.update_instances(instances),
        }
    }

    /// Unified select — returns a `RequestGuard` that auto-decrements
    /// counters on drop.
    pub fn select(self: &Arc<Self>) -> Option<RequestGuard> {
        match self.as_ref() {
            Self::RoundRobin(lb) => {
                let instance = lb.do_select()?;
                Some(RequestGuard {
                    instance,
                    _balancer: None,
                    _latency_guard: None,
                })
            }
            Self::Random(lb) => {
                let instance = lb.do_select()?;
                Some(RequestGuard {
                    instance,
                    _balancer: None,
                    _latency_guard: None,
                })
            }
            Self::LeastRequest(lb) => {
                let instance = lb.do_select()?;
                instance.inc_active();
                Some(RequestGuard {
                    instance,
                    _balancer: Some(self.clone()),
                    _latency_guard: None,
                })
            }
            Self::PeakEwma(lb) => {
                let latency_guard = lb.do_select()?;
                let instance = latency_guard.get_instance().clone();
                Some(RequestGuard {
                    instance,
                    _balancer: None,
                    _latency_guard: Some(latency_guard),
                })
            }
        }
    }

    pub fn get_instances(&self) -> Vec<UpstreamInstance> {
        match self {
            Self::RoundRobin(lb) => lb.get_instances(),
            Self::Random(lb) => lb.get_instances(),
            Self::LeastRequest(lb) => lb.get_instances(),
            Self::PeakEwma(lb) => lb.get_instances(),
        }
    }
}

/// RAII guard returned from `LoadBalancer::select()`.
/// Automatically decrements active counters on drop.
pub struct RequestGuard {
    pub instance: UpstreamInstance,
    pub(crate) _balancer: Option<Arc<LoadBalancer>>,
    pub(crate) _latency_guard: Option<LatencyGuard>,
}

impl RequestGuard {
    pub fn endpoint(&self) -> &str {
        self.instance.endpoint()
    }

    /// Mark request as failed (records penalty latency for PeakEWMA).
    pub fn mark_failed(&mut self) {
        if let Some(ref mut guard) = self._latency_guard {
            guard.mark_failed();
        }
    }
}

impl Drop for RequestGuard {
    fn drop(&mut self) {
        if self._balancer.is_some() {
            self.instance.dec_active();
        }
    }
}

/// Resolved upstream target metadata for building the proxy request.
///
/// Uses `Arc<str>` for `scheme` / `pass_host` / `upstream_host` because these
/// values come from the cluster config (rarely changes) and are cloned on every
/// request in `select_upstream()`. `Arc<str>` clone is just an atomic increment
/// vs `String::clone` which heap-allocates.
pub struct UpstreamTarget {
    pub instance: UpstreamInstance,
    pub scheme: Arc<str>,
    pub pass_host: Arc<str>,
    pub upstream_host: Option<Arc<str>>,
}
