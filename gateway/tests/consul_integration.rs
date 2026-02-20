//! Integration tests for the Consul discovery client.
//!
//! These tests require Docker (via testcontainers) and start a real
//! Consul agent in dev mode.
//!
//! Run with: `cargo test --test consul_integration`

use hermes_gateway::discovery::client::ConsulClient;
use serde::Serialize;
use std::collections::HashMap;

use testcontainers::core::IntoContainerPort;
use testcontainers::runners::AsyncRunner;
use testcontainers::{ContainerAsync, GenericImage, ImageExt};

/// Start a Consul container in dev mode and return (ConsulClient, base_url).
async fn start_consul() -> (ConsulClient, String, ContainerAsync<GenericImage>) {
    let container = GenericImage::new("hashicorp/consul", "1.19")
        .with_exposed_port(8500_u16.tcp())
        .with_env_var("CONSUL_BIND_INTERFACE", "eth0")
        .with_cmd(vec!["agent", "-dev", "-client=0.0.0.0"])
        .start()
        .await
        .expect("failed to start consul container");

    let host = container.get_host().await.expect("get host");
    let port = container.get_host_port_ipv4(8500).await.expect("get port");

    let base_url = format!("http://{}:{}", host, port);

    // Wait for Consul to be ready
    let http = reqwest::Client::new();
    for _ in 0..30 {
        if let Ok(resp) = http
            .get(format!("{}/v1/status/leader", base_url))
            .send()
            .await
        {
            if resp.status().is_success() {
                let body = resp.text().await.unwrap_or_default();
                if body.len() > 2 {
                    // leader is elected
                    break;
                }
            }
        }
        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
    }

    let client = ConsulClient::new(&base_url, None, None);

    (client, base_url, container)
}

#[derive(Serialize)]
struct TestServiceRegistration {
    #[serde(rename = "ID")]
    id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(rename = "Address")]
    address: String,
    #[serde(rename = "Port")]
    port: u16,
    #[serde(rename = "Meta")]
    meta: HashMap<String, String>,
    #[serde(rename = "Check")]
    check: TestTTLCheck,
}

#[derive(Serialize)]
struct TestTTLCheck {
    #[serde(rename = "CheckID")]
    check_id: String,
    #[serde(rename = "TTL")]
    ttl: String,
    #[serde(rename = "DeregisterCriticalServiceAfter")]
    deregister_after: String,
}

fn sample_registration(id: &str, name: &str, port: u16) -> TestServiceRegistration {
    TestServiceRegistration {
        id: id.to_string(),
        name: name.to_string(),
        address: "127.0.0.1".to_string(),
        port,
        meta: HashMap::from([("version".to_string(), "1.0".to_string())]),
        check: TestTTLCheck {
            check_id: id.to_string(),
            ttl: "30s".to_string(),
            deregister_after: "60s".to_string(),
        },
    }
}

// Tests
#[tokio::test]
async fn test_consul_register_and_query() {
    let (client, _base_url, _container) = start_consul().await;

    // Register a service
    let reg = sample_registration("svc-1", "my-service", 8080);
    client
        .register_service(&reg)
        .await
        .expect("register service");

    // Pass TTL so the service becomes healthy
    client.pass_ttl("svc-1").await.expect("pass TTL");

    // Query healthy services
    let nodes = client
        .query_healthy_services("my-service")
        .await
        .expect("query services");

    assert_eq!(nodes.len(), 1);
    assert_eq!(nodes[0].service_address, "127.0.0.1");
    assert_eq!(nodes[0].service_port, 8080);
    assert_eq!(nodes[0].service_meta.get("version").unwrap(), "1.0");
}

#[tokio::test]
async fn test_consul_register_multiple_instances() {
    let (client, _base_url, _container) = start_consul().await;

    // Register 3 instances of the same service
    for i in 1..=3 {
        let reg = sample_registration(&format!("multi-{}", i), "multi-service", 8080 + i);
        client.register_service(&reg).await.unwrap();
        client.pass_ttl(&format!("multi-{}", i)).await.unwrap();
    }

    let nodes = client
        .query_healthy_services("multi-service")
        .await
        .expect("query services");

    assert_eq!(nodes.len(), 3);

    let ports: Vec<u16> = nodes.iter().map(|n| n.service_port).collect();
    assert!(ports.contains(&8081));
    assert!(ports.contains(&8082));
    assert!(ports.contains(&8083));
}

#[tokio::test]
async fn test_consul_deregister() {
    let (client, _base_url, _container) = start_consul().await;

    // Register
    let reg = sample_registration("dereg-svc", "dereg-service", 9090);
    client.register_service(&reg).await.unwrap();
    client.pass_ttl("dereg-svc").await.unwrap();

    // Verify registered
    let nodes = client
        .query_healthy_services("dereg-service")
        .await
        .unwrap();
    assert_eq!(nodes.len(), 1);

    // Deregister
    client
        .deregister_service("dereg-svc")
        .await
        .expect("deregister");

    // Verify gone
    let nodes = client
        .query_healthy_services("dereg-service")
        .await
        .unwrap();
    assert_eq!(nodes.len(), 0);
}

#[tokio::test]
async fn test_consul_query_nonexistent_service() {
    let (client, _base_url, _container) = start_consul().await;

    let nodes = client
        .query_healthy_services("nonexistent-service")
        .await
        .expect("query nonexistent");

    assert_eq!(nodes.len(), 0);
}

#[tokio::test]
async fn test_consul_pass_ttl_nonexistent() {
    let (client, _base_url, _container) = start_consul().await;

    // Pass TTL for a service that doesn't exist — should return error
    let result = client.pass_ttl("nonexistent-check").await;
    assert!(result.is_err());
}

#[tokio::test]
async fn test_consul_unhealthy_service_not_returned() {
    let (client, _base_url, _container) = start_consul().await;

    // Register service but do NOT pass TTL
    let reg = sample_registration("unhealthy-svc", "unhealthy-service", 7070);
    client.register_service(&reg).await.unwrap();

    // Without passing TTL, the service check is critical.
    // query_healthy_services uses ?passing=true, so it should NOT return this service.
    let nodes = client
        .query_healthy_services("unhealthy-service")
        .await
        .unwrap();

    assert_eq!(nodes.len(), 0, "unhealthy service should not be returned");
}
