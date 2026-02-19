use super::UpstreamInstance;
use arc_swap::ArcSwap;
use rand::Rng;
use std::sync::atomic::Ordering;
use std::sync::Arc;

/// P2C (Power of Two Random Choices) Load Balancer.
///
/// Randomly pick two instances, compare `score = active_requests / weight`,
/// select the one with the lower score. O(1) per selection.
pub struct LeastRequestBalancer {
    instances: ArcSwap<Vec<UpstreamInstance>>,
}

impl Default for LeastRequestBalancer {
    fn default() -> Self {
        Self {
            instances: ArcSwap::from_pointee(Vec::new()),
        }
    }
}

impl LeastRequestBalancer {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn update_instances(&self, instances: Vec<UpstreamInstance>) {
        let old = self.instances.load();
        let new_instances: Vec<UpstreamInstance> = instances
            .into_iter()
            .map(|mut inst| {
                if let Some(existing) = old.iter().find(|e| e.endpoint() == inst.endpoint()) {
                    inst.active_requests = existing.active_requests.clone();
                }
                inst
            })
            .collect();
        self.instances.store(Arc::new(new_instances));
    }

    #[inline]
    fn score(inst: &UpstreamInstance) -> f64 {
        let active = inst.active_requests.load(Ordering::Relaxed);
        let weight = inst.weight.max(1) as f64;
        active as f64 / weight
    }

    pub fn do_select(&self) -> Option<UpstreamInstance> {
        let instances = self.instances.load();
        let len = instances.len();
        match len {
            0 => None,
            1 => Some(instances[0].clone()),
            _ => {
                let mut rng = rand::thread_rng();
                let idx1 = rng.gen_range(0..len);
                let idx2 = {
                    let offset = rng.gen_range(1..len);
                    (idx1 + offset) % len
                };
                if Self::score(&instances[idx1]) <= Self::score(&instances[idx2]) {
                    Some(instances[idx1].clone())
                } else {
                    Some(instances[idx2].clone())
                }
            }
        }
    }

    pub fn get_instances(&self) -> Vec<UpstreamInstance> {
        self.instances.load().as_ref().clone()
    }

    #[cfg(test)]
    pub fn get_active_count(&self, endpoint: &str) -> usize {
        self.instances
            .load()
            .iter()
            .find(|i| i.endpoint() == endpoint)
            .map(|i| i.active_requests.load(Ordering::Relaxed))
            .unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::upstream::loadbalance::LoadBalancer;
    use std::collections::HashMap;
    use std::sync::atomic::AtomicUsize;

    fn inst(host: &str, weight: u32) -> UpstreamInstance {
        let endpoint: Arc<str> = format!("{}:{}", host, 80).into();
        UpstreamInstance {
            host: host.to_string(),
            port: 80,
            weight,
            metadata: HashMap::new(),
            active_requests: Arc::new(AtomicUsize::new(0)),
            endpoint,
        }
    }

    #[test]
    fn test_single() {
        let lb = Arc::new(LoadBalancer::LeastRequest(LeastRequestBalancer::new()));
        lb.update_instances(&[crate::config::UpstreamNode {
            host: "A".to_string(),
            port: 80,
            weight: 100,
            metadata: HashMap::new(),
        }]);
        let guard = lb.select().unwrap();
        assert_eq!(guard.instance.host, "A");
    }

    #[test]
    fn test_p2c_selects_lower_load() {
        let lb = Arc::new(LoadBalancer::LeastRequest(LeastRequestBalancer::new()));
        lb.update_instances(&[
            crate::config::UpstreamNode {
                host: "A".to_string(),
                port: 80,
                weight: 100,
                metadata: HashMap::new(),
            },
            crate::config::UpstreamNode {
                host: "B".to_string(),
                port: 80,
                weight: 100,
                metadata: HashMap::new(),
            },
        ]);

        if let LoadBalancer::LeastRequest(inner) = lb.as_ref() {
            let instances = inner.get_instances();
            let inst_a = instances.iter().find(|i| i.host == "A").unwrap();
            for _ in 0..100 {
                inst_a.inc_active();
            }
        }

        let mut b_count = 0;
        for _ in 0..100 {
            let guard = lb.select().unwrap();
            if guard.instance.host == "B" {
                b_count += 1;
            }
        }
        assert_eq!(b_count, 100, "B should always win when A has high load");
    }

    #[test]
    fn test_guard_auto_release() {
        let inner = LeastRequestBalancer::new();
        inner.update_instances(vec![inst("A", 100)]);
        let lb = Arc::new(LoadBalancer::LeastRequest(inner));
        {
            let _guard = lb.select().unwrap();
            if let LoadBalancer::LeastRequest(inner) = lb.as_ref() {
                assert_eq!(inner.get_active_count("A:80"), 1);
            }
        }
        if let LoadBalancer::LeastRequest(inner) = lb.as_ref() {
            assert_eq!(inner.get_active_count("A:80"), 0);
        }
    }

    #[test]
    fn test_empty() {
        let lb = Arc::new(LoadBalancer::LeastRequest(LeastRequestBalancer::new()));
        lb.update_instances(&[]);
        assert!(lb.select().is_none());
    }

    #[test]
    fn test_counter_shared_across_refresh() {
        let inner = LeastRequestBalancer::new();
        inner.update_instances(vec![inst("A", 100)]);
        let instances = inner.get_instances();
        let a = instances.iter().find(|i| i.host == "A").unwrap();
        a.inc_active();
        assert_eq!(inner.get_active_count("A:80"), 1);

        inner.update_instances(vec![inst("A", 100)]);
        assert_eq!(inner.get_active_count("A:80"), 1);

        a.dec_active();
        assert_eq!(inner.get_active_count("A:80"), 0);
    }
}
