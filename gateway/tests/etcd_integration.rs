//! Integration tests for the etcd client and config sync modules.
//!
//! These tests require Docker (via testcontainers) and are skipped in
//! environments without Docker by simply failing at container startup.
//!
//! Run with: `cargo test --test etcd_integration`

use hermes_gateway::config::etcd::{compute_prefixes, initial_load, watch_once, ConfigEvent};
use hermes_gateway::config::EtcdConfig;
use hermes_gateway::etcd::client::{b64_encode, PutRequest};
use hermes_gateway::etcd::EtcdClient;

use testcontainers::core::IntoContainerPort;
use testcontainers::runners::AsyncRunner;
use testcontainers::{ContainerAsync, GenericImage, ImageExt};
use tokio::sync::mpsc;

/// Start an etcd container and return (EtcdClient, EtcdConfig).
async fn start_etcd() -> (EtcdClient, EtcdConfig, ContainerAsync<GenericImage>) {
    let container = GenericImage::new("quay.io/coreos/etcd", "v3.5.17")
        .with_exposed_port(2379_u16.tcp())
        .with_env_var("ETCD_ADVERTISE_CLIENT_URLS", "http://0.0.0.0:2379")
        .with_env_var("ETCD_LISTEN_CLIENT_URLS", "http://0.0.0.0:2379")
        .start()
        .await
        .expect("failed to start etcd container");

    let host = container.get_host().await.expect("get host");
    let port = container.get_host_port_ipv4(2379).await.expect("get port");

    let endpoint = format!("http://{}:{}", host, port);

    // Wait for etcd to be ready
    let http = reqwest::Client::new();
    for _ in 0..30 {
        if let Ok(resp) = http
            .post(format!("{}/v3/maintenance/status", endpoint))
            .json(&serde_json::json!({}))
            .send()
            .await
        {
            if resp.status().is_success() {
                break;
            }
        }
        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
    }

    let etcd_cfg = EtcdConfig {
        endpoints: vec![endpoint],
        domain_prefix: "/hermes/domains".to_string(),
        cluster_prefix: "/hermes/clusters".to_string(),
        meta_prefix: Some("/hermes/meta".to_string()),
        username: None,
        password: None,
    };

    let client = EtcdClient::connect(&etcd_cfg)
        .await
        .expect("connect to etcd");

    (client, etcd_cfg, container)
}

fn sample_domain_json(name: &str, host: &str) -> String {
    serde_json::json!({
        "name": name,
        "hosts": [host],
        "routes": [{
            "name": "catch-all",
            "uri": "/*",
            "clusters": [{"name": "backend", "weight": 100}],
            "status": 1
        }]
    })
    .to_string()
}

fn sample_cluster_json(name: &str) -> String {
    serde_json::json!({
        "name": name,
        "type": "roundrobin",
        "timeout": {"connect": 3.0, "send": 6.0, "read": 6.0},
        "nodes": [{"host": "10.0.0.1", "port": 8080, "weight": 100}]
    })
    .to_string()
}

// ── EtcdClient low-level tests ──────────────────────

#[tokio::test]
async fn test_etcd_put_and_range() {
    let (client, _cfg, _container) = start_etcd().await;

    // Put a key
    client
        .put(&PutRequest {
            key: b64_encode("/test/key1"),
            value: b64_encode("hello"),
            lease: None,
        })
        .await
        .expect("put");

    // Range query - exact key
    let resp = client
        .range(&hermes_gateway::etcd::client::RangeRequest {
            key: b64_encode("/test/key1"),
            range_end: String::new(),
            keys_only: None,
        })
        .await
        .expect("range");

    assert_eq!(resp.kvs.len(), 1);
    let val = hermes_gateway::etcd::client::b64_decode(&resp.kvs[0].value).unwrap();
    assert_eq!(val, "hello");
}

#[tokio::test]
async fn test_etcd_range_prefix() {
    let (client, _cfg, _container) = start_etcd().await;

    // Put multiple keys under a prefix
    for i in 0..3 {
        client
            .put(&PutRequest {
                key: b64_encode(&format!("/prefix/key{}", i)),
                value: b64_encode(&format!("val{}", i)),
                lease: None,
            })
            .await
            .expect("put");
    }

    // Range with prefix
    let resp = client
        .range(&hermes_gateway::etcd::client::RangeRequest {
            key: b64_encode("/prefix/"),
            range_end: hermes_gateway::etcd::client::prefix_range_end("/prefix/"),
            keys_only: None,
        })
        .await
        .expect("range prefix");

    assert_eq!(resp.kvs.len(), 3);
}

#[tokio::test]
async fn test_etcd_lease_grant_and_keepalive() {
    let (client, _cfg, _container) = start_etcd().await;

    let lease_id = client.lease_grant(30).await.expect("lease grant");
    assert!(lease_id > 0);

    // Keepalive should succeed
    client
        .lease_keepalive(lease_id)
        .await
        .expect("lease keepalive");

    // Put a key with lease
    client
        .put(&PutRequest {
            key: b64_encode("/leased/key"),
            value: b64_encode("leased-value"),
            lease: Some(lease_id),
        })
        .await
        .expect("put with lease");

    // Key should exist
    let resp = client
        .range(&hermes_gateway::etcd::client::RangeRequest {
            key: b64_encode("/leased/key"),
            range_end: String::new(),
            keys_only: None,
        })
        .await
        .expect("range");
    assert_eq!(resp.kvs.len(), 1);

    // Revoke lease — key should disappear
    client.lease_revoke(lease_id).await.expect("lease revoke");

    let resp = client
        .range(&hermes_gateway::etcd::client::RangeRequest {
            key: b64_encode("/leased/key"),
            range_end: String::new(),
            keys_only: None,
        })
        .await
        .expect("range after revoke");
    assert_eq!(resp.kvs.len(), 0, "key should be gone after lease revoke");
}

#[tokio::test]
async fn test_etcd_watch_stream() {
    let (client, _cfg, _container) = start_etcd().await;

    // Open a watch on /watch/ prefix
    let watch_client = client.clone();
    let (tx, _rx) = tokio::sync::oneshot::channel::<Vec<String>>();

    let watch_handle = tokio::spawn(async move {
        use hermes_gateway::etcd::client::{WatchCreate, WatchCreateRequest};

        let mut stream = watch_client
            .watch_stream(&WatchCreateRequest {
                create_request: WatchCreate {
                    key: b64_encode("/watch/"),
                    range_end: hermes_gateway::etcd::client::prefix_range_end("/watch/"),
                    start_revision: None,
                },
            })
            .await
            .expect("watch stream");

        let mut keys = Vec::new();
        // Read up to 2 events then stop
        for _ in 0..2 {
            if let Some(resp) = stream.next_response().await {
                if let Some(result) = resp.result {
                    for event in &result.events {
                        if let Some(kv) = &event.kv {
                            if let Ok(k) = hermes_gateway::etcd::client::b64_decode(&kv.key) {
                                keys.push(k);
                            }
                        }
                    }
                }
                if keys.len() >= 2 {
                    break;
                }
            }
        }
        let _ = tx.send(keys);
    });

    // Give watch time to establish
    tokio::time::sleep(std::time::Duration::from_millis(500)).await;

    // Write 2 keys
    client
        .put(&PutRequest {
            key: b64_encode("/watch/a"),
            value: b64_encode("1"),
            lease: None,
        })
        .await
        .unwrap();

    client
        .put(&PutRequest {
            key: b64_encode("/watch/b"),
            value: b64_encode("2"),
            lease: None,
        })
        .await
        .unwrap();

    // Wait for watch to collect events (with timeout)
    let result = tokio::time::timeout(std::time::Duration::from_secs(10), watch_handle).await;
    assert!(result.is_ok(), "watch timed out");
}

// ── Config etcd module tests (initial_load, watch_once) ─────────

#[tokio::test]
async fn test_initial_load_empty() {
    let (client, cfg, _container) = start_etcd().await;
    let prefixes = compute_prefixes(&cfg);

    let load = initial_load(&client, &prefixes)
        .await
        .expect("initial load");

    assert_eq!(load.domains.len(), 0);
    assert_eq!(load.clusters.len(), 0);
    assert_eq!(load.meta_revision, 0);
}

#[tokio::test]
async fn test_initial_load_with_data() {
    let (client, cfg, _container) = start_etcd().await;
    let prefixes = compute_prefixes(&cfg);

    // Write domain and cluster data to etcd
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/domains/api"),
            value: b64_encode(&sample_domain_json("api", "api.example.com")),
            lease: None,
        })
        .await
        .unwrap();

    client
        .put(&PutRequest {
            key: b64_encode("/hermes/domains/web"),
            value: b64_encode(&sample_domain_json("web", "web.example.com")),
            lease: None,
        })
        .await
        .unwrap();

    client
        .put(&PutRequest {
            key: b64_encode("/hermes/clusters/backend"),
            value: b64_encode(&sample_cluster_json("backend")),
            lease: None,
        })
        .await
        .unwrap();

    // Write meta revision
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/meta/config_revision"),
            value: b64_encode("42"),
            lease: None,
        })
        .await
        .unwrap();

    let load = initial_load(&client, &prefixes)
        .await
        .expect("initial load");

    assert_eq!(load.domains.len(), 2);
    assert_eq!(load.clusters.len(), 1);
    assert_eq!(load.meta_revision, 42);

    let domain_names: Vec<&str> = load.domains.iter().map(|d| d.name.as_str()).collect();
    assert!(domain_names.contains(&"api"));
    assert!(domain_names.contains(&"web"));
    assert_eq!(load.clusters[0].name, "backend");
}

#[tokio::test]
async fn test_initial_load_ignores_history_keys() {
    let (client, cfg, _container) = start_etcd().await;
    let prefixes = compute_prefixes(&cfg);

    // Write a normal domain
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/domains/api"),
            value: b64_encode(&sample_domain_json("api", "api.example.com")),
            lease: None,
        })
        .await
        .unwrap();

    // Write a history key (should be ignored)
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/domains/history/api/v1"),
            value: b64_encode(&sample_domain_json("api-old", "old.example.com")),
            lease: None,
        })
        .await
        .unwrap();

    let load = initial_load(&client, &prefixes)
        .await
        .expect("initial load");
    assert_eq!(load.domains.len(), 1, "history key should be filtered out");
    assert_eq!(load.domains[0].name, "api");
}

#[tokio::test]
async fn test_watch_once_receives_events() {
    let (client, cfg, _container) = start_etcd().await;
    let prefixes = compute_prefixes(&cfg);

    // First do initial load to get the current revision
    let load = initial_load(&client, &prefixes).await.unwrap();
    let start_revision = load.revision;

    // Start watching
    let (sender, mut receiver) = mpsc::unbounded_channel::<ConfigEvent>();
    let watch_client = client.clone();
    let watch_prefixes = prefixes;

    let watch_handle = tokio::spawn(async move {
        watch_once(&watch_client, &watch_prefixes, start_revision, sender).await
    });

    // Give watch time to establish
    tokio::time::sleep(std::time::Duration::from_millis(500)).await;

    // Write a domain
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/domains/live"),
            value: b64_encode(&sample_domain_json("live", "live.example.com")),
            lease: None,
        })
        .await
        .unwrap();

    // Write a cluster
    client
        .put(&PutRequest {
            key: b64_encode("/hermes/clusters/live-backend"),
            value: b64_encode(&sample_cluster_json("live-backend")),
            lease: None,
        })
        .await
        .unwrap();

    // Collect events with timeout
    let mut domain_events = Vec::new();
    let mut cluster_events = Vec::new();

    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(10);
    loop {
        tokio::select! {
            event = receiver.recv() => {
                match event {
                    Some(ConfigEvent::DomainUpsert(d)) => domain_events.push(d.name.clone()),
                    Some(ConfigEvent::ClusterUpsert(c)) => cluster_events.push(c.name.clone()),
                    _ => {}
                }
                if !domain_events.is_empty() && !cluster_events.is_empty() {
                    break;
                }
            }
            _ = tokio::time::sleep_until(deadline) => {
                break;
            }
        }
    }

    // Cancel watch
    watch_handle.abort();

    assert!(
        domain_events.contains(&"live".to_string()),
        "should receive domain upsert event"
    );
    assert!(
        cluster_events.contains(&"live-backend".to_string()),
        "should receive cluster upsert event"
    );
}

// ── Helper function tests (b64, prefix_range_end) ───────────────

#[test]
fn test_b64_roundtrip() {
    let original = "/hermes/domains/test-domain";
    let encoded = b64_encode(original);
    let decoded = hermes_gateway::etcd::client::b64_decode(&encoded).unwrap();
    assert_eq!(decoded, original);
}

#[test]
fn test_prefix_range_end() {
    let prefix = "/hermes/domains/";
    let end = hermes_gateway::etcd::client::prefix_range_end(prefix);
    // The range end should be the prefix with the last byte incremented
    assert!(!end.is_empty());
    // Decoding should give us the incremented prefix
    let decoded = hermes_gateway::etcd::client::b64_decode(&end).unwrap();
    assert!(decoded > prefix.to_string());
}
