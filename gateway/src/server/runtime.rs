/// Container-aware CPU limit detection for sizing the tokio worker thread pool.
///
/// Detection order:
/// 1. `MY_CPU_LIMIT` env var (explicit override, supports "4" or "4000m" format)
/// 2. cgroup v2: `/sys/fs/cgroup/cpu.max`
/// 3. cgroup v1: `/sys/fs/cgroup/cpu/cpu.cfs_quota_us` + `cpu.cfs_period_us`
/// 4. Fallback: `std::thread::available_parallelism()` (host CPU count)
///
/// Without this, tokio defaults to host CPU count, which over-provisions threads
/// when a container is limited to e.g. 4 cores on a 64-core host.
pub fn get_container_cpu_limit() -> usize {
    // 1. Explicit env var (set by k8s downward API or Dockerfile)
    if let Ok(cpu_limit) = std::env::var("MY_CPU_LIMIT") {
        if let Some(cores) = parse_cpu_value(&cpu_limit) {
            let threads = cores.max(1);
            eprintln!(
                "[runtime] Using CPU limit from MY_CPU_LIMIT: {} threads",
                threads
            );
            return threads;
        }
    }

    // 2. cgroup v2 (unified hierarchy)
    if let Ok(max) = std::fs::read_to_string("/sys/fs/cgroup/cpu.max") {
        if let Some(cores) = parse_cgroup_v2_cpu(&max) {
            let threads = cores.max(1);
            eprintln!(
                "[runtime] Using CPU limit from cgroup v2: {} threads",
                threads
            );
            return threads;
        }
    }

    // 3. cgroup v1 (legacy hierarchy)
    if let (Ok(quota), Ok(period)) = (
        std::fs::read_to_string("/sys/fs/cgroup/cpu/cpu.cfs_quota_us"),
        std::fs::read_to_string("/sys/fs/cgroup/cpu/cpu.cfs_period_us"),
    ) {
        if let Some(cores) = parse_cgroup_v1_cpu(&quota, &period) {
            let threads = cores.max(1);
            eprintln!(
                "[runtime] Using CPU limit from cgroup v1: {} threads",
                threads
            );
            return threads;
        }
    }

    // 4. Fallback: host CPU count
    let threads = std::thread::available_parallelism()
        .map(|p| p.get())
        .unwrap_or(1);
    eprintln!("[runtime] Using system CPU count: {} threads", threads);
    threads
}

/// Parse CPU value — supports "4" (cores) or "4000m" (millicores) format.
fn parse_cpu_value(value: &str) -> Option<usize> {
    let value = value.trim();
    if let Some(stripped) = value.strip_suffix('m') {
        stripped.parse::<usize>().ok().map(|m| m / 1000)
    } else {
        value.parse::<usize>().ok()
    }
}

/// Parse cgroup v2 `cpu.max` — format: "quota period" or "max period".
fn parse_cgroup_v2_cpu(content: &str) -> Option<usize> {
    let parts: Vec<&str> = content.split_whitespace().collect();
    if parts.len() >= 2 {
        if parts[0] == "max" {
            return None; // unlimited
        }
        let quota: i64 = parts[0].parse().ok()?;
        let period: i64 = parts[1].parse().ok()?;
        if quota > 0 && period > 0 {
            return Some((quota / period) as usize);
        }
    }
    None
}

/// Parse cgroup v1 `cpu.cfs_quota_us` / `cpu.cfs_period_us`.
fn parse_cgroup_v1_cpu(quota: &str, period: &str) -> Option<usize> {
    let quota: i64 = quota.trim().parse().ok()?;
    let period: i64 = period.trim().parse().ok()?;
    if quota > 0 && period > 0 {
        Some((quota / period) as usize)
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_cpu_value_cores() {
        assert_eq!(parse_cpu_value("4"), Some(4));
        assert_eq!(parse_cpu_value("1"), Some(1));
        assert_eq!(parse_cpu_value("  8  "), Some(8));
    }

    #[test]
    fn test_parse_cpu_value_millicores() {
        assert_eq!(parse_cpu_value("4000m"), Some(4));
        assert_eq!(parse_cpu_value("2000m"), Some(2));
        assert_eq!(parse_cpu_value("500m"), Some(0)); // 0.5 cores → 0, caller clamps to 1
    }

    #[test]
    fn test_parse_cgroup_v2_cpu() {
        assert_eq!(parse_cgroup_v2_cpu("400000 100000"), Some(4));
        assert_eq!(parse_cgroup_v2_cpu("200000 100000"), Some(2));
        assert_eq!(parse_cgroup_v2_cpu("max 100000"), None);
        assert_eq!(parse_cgroup_v2_cpu(""), None);
    }

    #[test]
    fn test_parse_cgroup_v1_cpu() {
        assert_eq!(parse_cgroup_v1_cpu("400000", "100000"), Some(4));
        assert_eq!(parse_cgroup_v1_cpu("-1", "100000"), None);
        assert_eq!(parse_cgroup_v1_cpu("0", "100000"), None);
    }
}
