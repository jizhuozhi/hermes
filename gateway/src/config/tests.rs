use super::types::*;
use super::GatewayConfig;
use std::path::Path;

#[test]
fn test_load_toml_config() {
    let cfg = GatewayConfig::load(Path::new("config.toml")).unwrap();
    assert!(!cfg.consul.address.is_empty());
    assert!(!cfg.etcd.endpoints.is_empty());
}

#[test]
fn test_load_json_infra_config() {
    let json = r#"{
        "consul": { "address": "http://127.0.0.1:8500" },
        "etcd": { "endpoints": ["http://127.0.0.1:2379"] }
    }"#;
    let tmp = std::env::temp_dir().join("hermes_test_infra_config.json");
    std::fs::write(&tmp, json).unwrap();
    let cfg = GatewayConfig::load(&tmp).unwrap();
    assert_eq!(cfg.consul.address, "http://127.0.0.1:8500");
    assert_eq!(cfg.etcd.endpoints.len(), 1);
    std::fs::remove_file(&tmp).ok();
}

#[test]
fn test_validate_empty_etcd_endpoint_fails() {
    let cfg = GatewayConfig {
        etcd: EtcdConfig {
            endpoints: vec!["".into()],
            ..EtcdConfig::default()
        },
        ..GatewayConfig::default()
    };
    assert!(cfg.validate().is_err());
}

#[test]
fn test_validate_infra_only_ok() {
    let cfg = GatewayConfig::default();
    assert!(cfg.validate().is_ok());
}

#[test]
fn test_deserialize_defaults() {
    let toml_str = r#"
[consul]
address = "http://custom:8500"
"#;
    let cfg: GatewayConfig = toml::from_str(toml_str).unwrap();
    assert_eq!(cfg.consul.address, "http://custom:8500");
    assert_eq!(cfg.consul.poll_interval_secs, 10);
    assert_eq!(cfg.etcd.domain_prefix, "/hermes/domains");
    assert_eq!(cfg.etcd.cluster_prefix, "/hermes/clusters");
    assert!(!cfg.registration.enabled);
    assert!(!cfg.instance_registry.enabled);
}

#[test]
fn test_cluster_config_serde() {
    let json = r#"{
        "name": "backend",
        "type": "least_request",
        "timeout": {"connect": 3.0, "send": 5.0, "read": 10.0},
        "scheme": "https",
        "pass_host": "rewrite",
        "upstream_host": "api.internal",
        "nodes": [
            {"host": "10.0.0.1", "port": 8080, "weight": 100},
            {"host": "10.0.0.2", "port": 8080, "weight": 50}
        ],
        "keepalive_pool": {"idle_timeout": 30, "requests": 500, "size": 64},
        "retry": {"count": 3, "retry_on_statuses": [502, 503]},
        "circuit_breaker": {"failure_threshold": 10, "success_threshold": 3, "open_duration_secs": 60}
    }"#;
    let cluster: ClusterConfig = serde_json::from_str(json).unwrap();
    assert_eq!(cluster.name, "backend");
    assert_eq!(cluster.lb_type, "least_request");
    assert_eq!(cluster.scheme, "https");
    assert_eq!(cluster.pass_host, "rewrite");
    assert_eq!(cluster.upstream_host, Some("api.internal".to_string()));
    assert_eq!(cluster.nodes.len(), 2);
    assert_eq!(cluster.timeout.connect, 3.0);
    assert_eq!(cluster.timeout.read, 10.0);
    assert_eq!(cluster.keepalive_pool.size, 64);
    assert_eq!(cluster.retry.as_ref().unwrap().count, 3);
    assert_eq!(
        cluster.circuit_breaker.as_ref().unwrap().failure_threshold,
        10
    );
}

#[test]
fn test_rate_limit_config_serde() {
    let json = r#"{"mode": "count", "count": 1000, "time_window": 60, "key": "route", "rejected_code": 503}"#;
    let rl: RateLimitConfig = serde_json::from_str(json).unwrap();
    assert_eq!(rl.mode, "count");
    assert_eq!(rl.count, Some(1000));
    assert_eq!(rl.time_window, Some(60));
    assert_eq!(rl.key, "route");
    assert_eq!(rl.rejected_code, 503);
}

#[test]
fn test_header_matcher_defaults() {
    let json = r#"{"name": "X-Canary", "value": "true"}"#;
    let hm: HeaderMatcher = serde_json::from_str(json).unwrap();
    assert_eq!(hm.match_type, "exact");
    assert!(!hm.invert);
}

#[test]
fn test_unsupported_format() {
    let tmp = std::env::temp_dir().join("test.yml");
    std::fs::write(&tmp, "key: value").unwrap();
    assert!(GatewayConfig::load(&tmp).is_err());
    std::fs::remove_file(&tmp).ok();
}

/// Ensure unknown fields (e.g. old `domains`/`clusters` in config file) are
/// silently ignored rather than causing a parse error, so existing config
/// files with leftover business-config sections still work.
#[test]
fn test_ignores_unknown_fields_in_toml() {
    let toml_str = r#"
[consul]
address = "http://127.0.0.1:8500"

[[domains]]
name = "leftover"
hosts = ["example.com"]

[[clusters]]
name = "leftover"
"#;
    // serde(default) + deny_unknown_fields is NOT set, so this should parse fine.
    let cfg: GatewayConfig = toml::from_str(toml_str).unwrap();
    assert_eq!(cfg.consul.address, "http://127.0.0.1:8500");
}
