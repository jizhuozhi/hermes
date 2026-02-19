use super::UpstreamInstance;
use arc_swap::ArcSwap;
use rand::Rng;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

/// Peak EWMA Load Balancer with P2C selection.
///
/// Score = `ewma_latency * (active_requests + 1) / weight`. Lower is better.
/// EWMA is updated via `alpha * new + (1 - alpha) * old` on each completed request.
pub struct PeakEwmaBalancer {
    instances: ArcSwap<Vec<InstanceWithLatency>>,
    alpha: f64,
    initial_latency_ns: u64,
}

#[derive(Clone)]
pub struct InstanceWithLatency {
    pub instance: UpstreamInstance,
    ewma_latency_ns: Arc<AtomicU64>,
}

impl InstanceWithLatency {
    fn new(instance: UpstreamInstance, initial_latency_ns: u64) -> Self {
        Self {
            instance,
            ewma_latency_ns: Arc::new(AtomicU64::new(
                (initial_latency_ns as f64).to_bits(),
            )),
        }
    }

    #[inline]
    fn get_ewma_latency(&self) -> f64 {
        f64::from_bits(self.ewma_latency_ns.load(Ordering::Relaxed))
    }

    #[inline]
    fn update_latency(&self, new_latency_ns: u64, alpha: f64) {
        let new_latency = new_latency_ns as f64;
        let current = f64::from_bits(self.ewma_latency_ns.load(Ordering::Relaxed));
        let new_ewma = alpha * new_latency + (1.0 - alpha) * current;
        self.ewma_latency_ns
            .store(new_ewma.to_bits(), Ordering::Relaxed);
    }

    #[inline]
    fn score(&self) -> f64 {
        let ewma = self.get_ewma_latency();
        let active = self.instance.active_requests.load(Ordering::Relaxed) as f64;
        let weight = self.instance.weight.max(1) as f64;
        (ewma * (active + 1.0)) / weight
    }
}

impl PeakEwmaBalancer {
    pub fn new(alpha: f64, initial_latency_ms: u64) -> Self {
        Self {
            instances: ArcSwap::from_pointee(Vec::new()),
            alpha: alpha.clamp(0.01, 1.0),
            initial_latency_ns: initial_latency_ms * 1_000_000,
        }
    }

    pub fn new_default() -> Self {
        Self::new(0.2, 20)
    }

    pub fn update_instances(&self, instances: Vec<UpstreamInstance>) {
        let old = self.instances.load();
        let new_instances: Vec<InstanceWithLatency> = instances
            .into_iter()
            .map(|mut inst| {
                if let Some(existing) = old
                    .iter()
                    .find(|e| e.instance.endpoint() == inst.endpoint())
                {
                    inst.active_requests = existing.instance.active_requests.clone();
                    InstanceWithLatency {
                        instance: inst,
                        ewma_latency_ns: existing.ewma_latency_ns.clone(),
                    }
                } else {
                    InstanceWithLatency::new(inst, self.initial_latency_ns)
                }
            })
            .collect();
        self.instances.store(Arc::new(new_instances));
    }

    pub fn do_select(&self) -> Option<LatencyGuard> {
        let instances = self.instances.load();
        let len = instances.len();
        match len {
            0 => None,
            1 => Some(LatencyGuard {
                instance_with_latency: instances[0].clone(),
                start_time: Instant::now(),
                alpha: self.alpha,
                failed: false,
            }),
            _ => {
                let mut rng = rand::thread_rng();
                let idx1 = rng.gen_range(0..len);
                let idx2 = {
                    let offset = rng.gen_range(1..len);
                    (idx1 + offset) % len
                };
                let selected = if instances[idx1].score() <= instances[idx2].score() {
                    &instances[idx1]
                } else {
                    &instances[idx2]
                };
                Some(LatencyGuard {
                    instance_with_latency: selected.clone(),
                    start_time: Instant::now(),
                    alpha: self.alpha,
                    failed: false,
                })
            }
        }
    }

    pub fn get_instances(&self) -> Vec<UpstreamInstance> {
        self.instances
            .load()
            .iter()
            .map(|i| i.instance.clone())
            .collect()
    }

    #[cfg(test)]
    pub fn get_ewma_latency_ms(&self, endpoint: &str) -> Option<f64> {
        self.instances
            .load()
            .iter()
            .find(|i| i.instance.endpoint() == endpoint)
            .map(|i| i.get_ewma_latency() / 1_000_000.0)
    }
}

/// RAII guard that records latency on drop.
pub struct LatencyGuard {
    instance_with_latency: InstanceWithLatency,
    start_time: Instant,
    alpha: f64,
    failed: bool,
}

impl LatencyGuard {
    pub fn get_instance(&self) -> &UpstreamInstance {
        &self.instance_with_latency.instance
    }

    pub fn mark_failed(&mut self) {
        self.failed = true;
    }
}

impl Drop for LatencyGuard {
    fn drop(&mut self) {
        let latency_ns = if self.failed {
            30_000_000_000u64 // 30s penalty
        } else {
            self.start_time.elapsed().as_nanos() as u64
        };
        self.instance_with_latency
            .update_latency(latency_ns, self.alpha);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::sync::atomic::AtomicUsize;
    use std::thread;
    use std::time::Duration;

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
    fn test_empty() {
        let lb = PeakEwmaBalancer::new_default();
        lb.update_instances(vec![]);
        assert!(lb.do_select().is_none());
    }

    #[test]
    fn test_single() {
        let lb = PeakEwmaBalancer::new_default();
        lb.update_instances(vec![inst("A", 100)]);
        let guard = lb.do_select().unwrap();
        assert_eq!(guard.get_instance().host, "A");
    }

    #[test]
    fn test_latency_preference() {
        let lb = PeakEwmaBalancer::new(0.5, 10);
        lb.update_instances(vec![inst("fast", 100), inst("slow", 100)]);

        // Simulate fast and slow latencies
        {
            let _g = lb.do_select().unwrap();
            thread::sleep(Duration::from_millis(5));
        }
        // Set slow node's latency high
        {
            let instances = lb.instances.load();
            let slow = instances
                .iter()
                .find(|i| i.instance.host == "slow")
                .unwrap();
            slow.update_latency(100_000_000, 0.9); // 100ms
        }

        let mut fast_count = 0;
        for _ in 0..50 {
            let guard = lb.do_select().unwrap();
            if guard.get_instance().host == "fast" {
                fast_count += 1;
            }
            drop(guard);
        }
        assert!(fast_count > 30, "fast should be preferred, got {}", fast_count);
    }

    #[test]
    fn test_active_requests_penalty() {
        let lb = PeakEwmaBalancer::new_default();
        lb.update_instances(vec![inst("A", 100), inst("B", 100)]);

        let instances = lb.instances.load();
        let inst_a = instances
            .iter()
            .find(|i| i.instance.host == "A")
            .unwrap();
        for _ in 0..10 {
            inst_a.instance.inc_active();
        }

        let mut b_count = 0;
        for _ in 0..50 {
            let guard = lb.do_select().unwrap();
            if guard.get_instance().host == "B" {
                b_count += 1;
            }
            drop(guard);
        }
        assert!(b_count > 40, "B should be heavily preferred, got {}", b_count);
    }

    #[test]
    fn test_counter_shared_across_refresh() {
        let lb = PeakEwmaBalancer::new_default();
        lb.update_instances(vec![inst("A", 100)]);

        {
            let _guard = lb.do_select().unwrap();
            thread::sleep(Duration::from_millis(30));
        }
        let latency_before = lb.get_ewma_latency_ms("A:80").unwrap();

        lb.update_instances(vec![inst("A", 100)]);
        let latency_after = lb.get_ewma_latency_ms("A:80").unwrap();
        assert!(
            (latency_before - latency_after).abs() < 0.1,
            "before={}, after={}",
            latency_before,
            latency_after
        );
    }
}
