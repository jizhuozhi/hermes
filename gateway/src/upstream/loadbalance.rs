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

    pub fn update_instances(&self, nodes: &[UpstreamNode]) {
        let instances: Vec<UpstreamInstance> = nodes.iter().map(UpstreamInstance::from).collect();
        match self {
            Self::RoundRobin(lb) => lb.update_instances(instances),
            Self::Random(lb) => lb.update_instances(instances),
            Self::LeastRequest(lb) => lb.update_instances(instances),
            Self::PeakEwma(lb) => lb.update_instances(instances),
        }
    }

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

/// RAII guard — automatically decrements active counters on drop.
pub struct RequestGuard {
    pub instance: UpstreamInstance,
    pub(crate) _balancer: Option<Arc<LoadBalancer>>,
    pub(crate) _latency_guard: Option<LatencyGuard>,
}

impl RequestGuard {
    pub fn endpoint(&self) -> &str {
        self.instance.endpoint()
    }

    /// Records penalty latency for PeakEWMA on failure.
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
/// Uses `Arc<str>` since these values come from config (rarely changes)
/// but are cloned on every request.
pub struct UpstreamTarget {
    pub instance: UpstreamInstance,
    pub scheme: Arc<str>,
    pub pass_host: Arc<str>,
    pub upstream_host: Option<Arc<str>>,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::UpstreamNode;
    use std::collections::HashMap;

    fn make_nodes(n: usize) -> Vec<UpstreamNode> {
        (0..n)
            .map(|i| UpstreamNode {
                host: format!("10.0.0.{}", i + 1),
                port: 8080,
                weight: 100,
                metadata: HashMap::new(),
            })
            .collect()
    }

    #[test]
    fn test_upstream_instance_from_node() {
        let node = UpstreamNode {
            host: "192.168.1.1".into(),
            port: 9090,
            weight: 50,
            metadata: [("env".into(), "prod".into())].into_iter().collect(),
        };
        let instance = UpstreamInstance::from(&node);
        assert_eq!(instance.host, "192.168.1.1");
        assert_eq!(instance.port, 9090);
        assert_eq!(instance.weight, 50);
        assert_eq!(instance.endpoint(), "192.168.1.1:9090");
        assert_eq!(instance.metadata["env"], "prod");
    }

    #[test]
    fn test_upstream_instance_active_requests() {
        let node = UpstreamNode {
            host: "h".into(),
            port: 80,
            weight: 1,
            metadata: HashMap::new(),
        };
        let inst = UpstreamInstance::from(&node);
        assert_eq!(
            inst.active_requests
                .load(std::sync::atomic::Ordering::Relaxed),
            0
        );
        inst.inc_active();
        inst.inc_active();
        assert_eq!(
            inst.active_requests
                .load(std::sync::atomic::Ordering::Relaxed),
            2
        );
        inst.dec_active();
        assert_eq!(
            inst.active_requests
                .load(std::sync::atomic::Ordering::Relaxed),
            1
        );
    }

    #[test]
    fn test_load_balancer_new_variants() {
        let _rr = LoadBalancer::new("roundrobin");
        let _rr2 = LoadBalancer::new("unknown_defaults_to_rr");
        let _rand = LoadBalancer::new("random");
        let _rand2 = LoadBalancer::new("weighted_random");
        let _lr = LoadBalancer::new("least_request");
        let _lr2 = LoadBalancer::new("least_conn");
        let _pe = LoadBalancer::new("peak_ewma");
        let _pe2 = LoadBalancer::new("ewma");
    }

    #[test]
    fn test_select_empty_returns_none() {
        for lb_type in &["roundrobin", "random", "least_request", "peak_ewma"] {
            let lb = LoadBalancer::new(lb_type);
            assert!(
                lb.select().is_none(),
                "empty {} should return None",
                lb_type
            );
        }
    }

    #[test]
    fn test_roundrobin_selects_all_nodes() {
        let lb = LoadBalancer::new("roundrobin");
        let nodes = make_nodes(3);
        lb.update_instances(&nodes);

        let mut seen = std::collections::HashSet::new();
        for _ in 0..300 {
            let guard = lb.select().unwrap();
            seen.insert(guard.endpoint().to_string());
        }
        assert_eq!(seen.len(), 3);
    }

    #[test]
    fn test_random_selects_nodes() {
        let lb = LoadBalancer::new("random");
        let nodes = make_nodes(3);
        lb.update_instances(&nodes);

        let mut seen = std::collections::HashSet::new();
        for _ in 0..100 {
            let guard = lb.select().unwrap();
            seen.insert(guard.endpoint().to_string());
        }
        assert_eq!(seen.len(), 3);
    }

    #[test]
    fn test_least_request_selects_and_tracks_active() {
        let lb = LoadBalancer::new("least_request");
        let nodes = make_nodes(2);
        lb.update_instances(&nodes);

        let guard1 = lb.select().unwrap();
        let ep1 = guard1.endpoint().to_string();

        let guard2 = lb.select().unwrap();
        let ep2 = guard2.endpoint().to_string();
        assert_ne!(ep1, ep2, "should pick the node with fewer active requests");

        drop(guard1);
    }

    #[test]
    fn test_peak_ewma_selects_nodes() {
        let lb = LoadBalancer::new("peak_ewma");
        let nodes = make_nodes(2);
        lb.update_instances(&nodes);

        let guard = lb.select().unwrap();
        assert!(!guard.endpoint().is_empty());
    }

    #[test]
    fn test_request_guard_decrements_on_drop_for_least_request() {
        let lb = LoadBalancer::new("least_request");
        let nodes = make_nodes(1);
        lb.update_instances(&nodes);

        let instances = lb.get_instances();
        let inst = &instances[0];

        {
            let _guard = lb.select().unwrap();
            assert_eq!(
                inst.active_requests
                    .load(std::sync::atomic::Ordering::Relaxed),
                1
            );
        }
        assert_eq!(
            inst.active_requests
                .load(std::sync::atomic::Ordering::Relaxed),
            0
        );
    }

    #[test]
    fn test_request_guard_mark_failed() {
        let lb = LoadBalancer::new("peak_ewma");
        let nodes = make_nodes(1);
        lb.update_instances(&nodes);

        let mut guard = lb.select().unwrap();
        guard.mark_failed();
    }

    #[test]
    fn test_update_instances_replaces_list() {
        let lb = LoadBalancer::new("roundrobin");
        lb.update_instances(&make_nodes(3));
        assert_eq!(lb.get_instances().len(), 3);

        lb.update_instances(&make_nodes(1));
        assert_eq!(lb.get_instances().len(), 1);

        lb.update_instances(&[]);
        assert_eq!(lb.get_instances().len(), 0);
        assert!(lb.select().is_none());
    }

    #[test]
    fn test_get_instances_returns_correct_data() {
        let lb = LoadBalancer::new("roundrobin");
        let nodes = make_nodes(2);
        lb.update_instances(&nodes);

        let instances = lb.get_instances();
        assert_eq!(instances.len(), 2);
        assert_eq!(instances[0].host, "10.0.0.1");
        assert_eq!(instances[1].host, "10.0.0.2");
    }

    #[test]
    fn test_upstream_target_fields() {
        let node = UpstreamNode {
            host: "h".into(),
            port: 80,
            weight: 1,
            metadata: HashMap::new(),
        };
        let target = UpstreamTarget {
            instance: UpstreamInstance::from(&node),
            scheme: Arc::from("https"),
            pass_host: Arc::from("rewrite"),
            upstream_host: Some(Arc::from("api.internal")),
        };
        assert_eq!(target.instance.endpoint(), "h:80");
        assert_eq!(&*target.scheme, "https");
        assert_eq!(&*target.pass_host, "rewrite");
        assert_eq!(target.upstream_host.as_deref(), Some("api.internal"));
    }
}
