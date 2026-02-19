use super::UpstreamInstance;
use arc_swap::ArcSwap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

/// Weighted Round-Robin using prefix sum + modular counter.
pub struct RoundRobinBalancer {
    state: ArcSwap<BalancerState>,
    counter: AtomicU64,
}

struct BalancerState {
    instances: Vec<UpstreamInstance>,
    prefix_sum: Vec<u64>,
    total_weight: u64,
}

impl Default for RoundRobinBalancer {
    fn default() -> Self {
        Self {
            state: ArcSwap::from_pointee(BalancerState {
                instances: Vec::new(),
                prefix_sum: Vec::new(),
                total_weight: 0,
            }),
            counter: AtomicU64::new(0),
        }
    }
}

impl RoundRobinBalancer {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn update_instances(&self, instances: Vec<UpstreamInstance>) {
        let mut prefix_sum = Vec::with_capacity(instances.len());
        let mut sum: u64 = 0;
        for inst in &instances {
            sum += inst.weight.max(1) as u64;
            prefix_sum.push(sum);
        }
        self.state.store(Arc::new(BalancerState {
            instances,
            prefix_sum,
            total_weight: sum,
        }));
    }

    pub fn do_select(&self) -> Option<UpstreamInstance> {
        let state = self.state.load();
        if state.total_weight == 0 {
            return None;
        }
        let count = self.counter.fetch_add(1, Ordering::Relaxed);
        let target = count % state.total_weight;
        let idx = state.prefix_sum.partition_point(|&s| s <= target);
        Some(state.instances[idx].clone())
    }

    pub fn get_instances(&self) -> Vec<UpstreamInstance> {
        self.state.load().instances.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn inst(host: &str, weight: u32) -> UpstreamInstance {
        let endpoint: Arc<str> = format!("{}:{}", host, 80).into();
        UpstreamInstance {
            host: host.to_string(),
            port: 80,
            weight,
            metadata: HashMap::new(),
            active_requests: Arc::new(std::sync::atomic::AtomicUsize::new(0)),
            endpoint,
        }
    }

    #[test]
    fn test_uniform() {
        let lb = RoundRobinBalancer::new();
        lb.update_instances(vec![inst("A", 100), inst("B", 100)]);
        let mut counts = HashMap::new();
        for _ in 0..1000 {
            let i = lb.do_select().unwrap();
            *counts.entry(i.host.clone()).or_insert(0) += 1;
        }
        assert_eq!(counts["A"], 500);
        assert_eq!(counts["B"], 500);
    }

    #[test]
    fn test_weighted() {
        let lb = RoundRobinBalancer::new();
        lb.update_instances(vec![inst("A", 2), inst("B", 3)]);
        let mut counts = HashMap::new();
        for _ in 0..5000 {
            let i = lb.do_select().unwrap();
            *counts.entry(i.host.clone()).or_insert(0) += 1;
        }
        assert_eq!(counts["A"], 2000);
        assert_eq!(counts["B"], 3000);
    }

    #[test]
    fn test_empty() {
        let lb = RoundRobinBalancer::new();
        lb.update_instances(vec![]);
        assert!(lb.do_select().is_none());
    }

    #[test]
    fn test_single() {
        let lb = RoundRobinBalancer::new();
        lb.update_instances(vec![inst("A", 100)]);
        for _ in 0..100 {
            assert_eq!(lb.do_select().unwrap().host, "A");
        }
    }
}
