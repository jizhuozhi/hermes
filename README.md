# Hermes

[![CI](https://github.com/jizhuozhi/hermes/actions/workflows/ci.yml/badge.svg)](https://github.com/jizhuozhi/hermes/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/jizhuozhi/hermes/graph/badge.svg)](https://codecov.io/gh/jizhuozhi/hermes)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Rust](https://img.shields.io/badge/Rust-2021_Edition-F74C00?logo=rust&logoColor=white)](https://www.rust-lang.org)
[![License](https://img.shields.io/github/license/jizhuozhi/hermes)](LICENSE)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/banner-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="assets/banner-light.svg">
  <img alt="Hermes banner" src="assets/banner-light.svg" width="100%">
</picture>

Hermes is a high-performance API gateway platform with a dynamic control plane. It features a **Rust data plane** for low-latency proxying, a **Go control plane** with a built-in web UI for configuration management, and a **Go controller** that bridges them via etcd.

## Architecture

```
                          ┌──────────────────────┐
                          │       Server         │
                          │   (Go + PostgreSQL)  │
              ┌──────────►│   + Web UI (Vue 3)   │◄──────────┐
              │  REST API │                      │ REST API  │
              │           └──────────┬───────────┘           │
              │                      │                       │
         OIDC / HMAC           Config Watch             Status Report
              │                      │                       │
              │                      ▼                       │
       ┌──────┴──────┐       ┌──────────────┐        ┌───────┴───────┐
       │   Browser   │       │  Controller  │        │   Controller  │
       │             │       │     (Go)     │        │   heartbeat   │
       └─────────────┘       └──────┬───────┘        └───────────────┘
                                    │
                            Put/Delete config
                                    │
                                    ▼
                              ┌──────────┐
                              │   etcd   │
                              └──────┬───┘
                                     │ watch
                                     ▼
                              ┌──────────────┐
                              │   Gateway    │        ┌────────┐
                              │   (Rust)     │◄──────►│ Consul │
                              │              │  svc   └────────┘
                              └──────┬───────┘ discovery
                                     │
                               Proxy requests
                                     │
                                     ▼
                              ┌──────────────┐
                              │  Upstreams   │
                              └──────────────┘
```

**Data flow:** UI/API → Server (PostgreSQL) → Controller (short-poll) → etcd → Gateway (watch) → hot reload with zero downtime.

## Features

### Gateway (Rust Data Plane)

- **High-performance proxy** — built on tokio + hyper, with jemalloc and zero-copy streaming
- **Dynamic routing** — Host-based domain partitioning with radix tree URI matching; supports exact, prefix wildcard (`/api/*`), host wildcards (`*.example.com`), method and header matching
- **Load balancing** — Round Robin (weighted), Random, Least Request, Peak EWMA
- **Service discovery** — Consul integration with metadata filtering and periodic polling
- **Circuit breaker** — Per-node circuit breaker with configurable thresholds and half-open probing
- **Rate limiting** — Token bucket with distributed quota splitting across gateway instances
- **Health checks** — Active (HTTP probing) and passive (response-based) health checking
- **Retry** — Configurable retry with status code filtering and node avoidance
- **Connection pooling** — Per-cluster HTTP connection pools with configurable idle timeout and pool size
- **Prometheus metrics** — Request latency, upstream latency, in-flight requests, rate limit counters, circuit breaker state, Consul discovery stats, and more
- **Instance self-registration** — Registers in etcd with lease TTL for peer discovery
- **Consul self-registration** — Optionally registers as a discoverable service in Consul
- **Hot config reload** — etcd watch with ArcSwap for lock-free config reads

### Server (Go + Vue 3)

- **Web UI** — Built-in Vue 3 SPA for managing domains, clusters, credentials, members, and monitoring
- **Multi-namespace** — All resources are namespace-scoped with namespace switching in the UI
- **RBAC** — Three roles (Owner, Editor, Viewer) with 13 fine-grained permission scopes
- **OIDC authentication** — Standard Authorization Code Flow; works with any OIDC provider (Keycloak, Okta, etc.)
- **HMAC-SHA256 authentication** — For service-to-service communication (Controller → Server)
- **OIDC Group Binding** — Map IdP groups to namespace roles automatically
- **Config versioning & rollback** — Full history with one-click rollback to any previous version
- **Audit log** — Records every config change with operator, timestamp, and action
- **Watch API** — Short-poll endpoint for controllers to receive incremental config changes
- **Status dashboard** — Real-time view of gateway instances and controller health
- **Grafana integration** — Embed Grafana dashboards per namespace
- **Bootstrap mode** — Unauthenticated access when no credentials exist (first-time setup)

### Controller (Go Config Sync)

- **Incremental sync** — Short-polls the server every few seconds for config changes, applies them to etcd
- **Full reconciliation** — Periodic full diff (server vs etcd) to self-heal any drift
- **Heartbeat** — Reports controller health and config revision to the server
- **Instance reporting** — Watches etcd for gateway instance registrations and reports them upstream
- **HMAC transport** — Automatically signs all requests to the server

## Prerequisites

- **etcd** 3.5+
- **PostgreSQL** 14+
- **Consul** (optional, for service discovery)
- **Go** 1.22+ (to build controller and server)
- **Rust** 1.75+ (to build gateway)
- **Node.js** 18+ (to build the web UI)

## Quick Start

### 1. Start Dependencies

```bash
# etcd
etcd --listen-client-urls http://0.0.0.0:2379 \
     --advertise-client-urls http://127.0.0.1:2379

# PostgreSQL (create the database)
createdb hermes

# Consul (optional)
consul agent -dev
```

### 2. Build & Run Server

```bash
cd server

# Build the web UI
cd web && npm install && npm run build && cd ..

# Copy and edit config
cp config.yaml.example config.yaml
# Edit config.yaml: set postgres DSN, OIDC settings, etc.

# Build and run
make build
./hermes-server -config config.yaml
```

The server starts on `http://localhost:9080` with the web UI.

### 3. Build & Run Controller

```bash
cd controller

# Copy and edit config
cp config.yaml.example config.yaml
# Edit config.yaml: set server URL, etcd endpoints, auth credentials
# Create credentials via the server UI first (Settings → Credentials)

# Build and run
make build
./controller -config config.yaml
```

### 4. Build & Run Gateway

```bash
cd gateway

# Copy and edit config
cp config.toml.example config.toml
# Edit config.toml: set etcd endpoints, Consul address

# Build and run
make release
./target/release/hermes-gateway --config config.toml
```

## Configuration

### Server (`server/config.yaml`)

```yaml
server:
  listen: "0.0.0.0:9080"

postgres:
  dsn: "postgres://postgres@localhost:5432/hermes?sslmode=disable"

oidc:
  enabled: false
  issuer: "https://your-oidc-provider.example.com/realms/hermes"
  client_id: "hermes"
  client_secret: "YOUR_OIDC_CLIENT_SECRET"
  # initial_admin_users: "alice@example.com,bob"
```

### Controller (`controller/config.yaml`)

```yaml
controlplane:
  url: "http://127.0.0.1:9080"
  poll_interval: 5
  reconcile_interval: 60

etcd:
  endpoints:
    - "http://127.0.0.1:2379"
  domain_prefix: "/hermes/domains"
  cluster_prefix: "/hermes/clusters"
  instance_prefix: "/hermes/instances"

auth:
  access_key: "YOUR_ACCESS_KEY"
  secret_key: "YOUR_SECRET_KEY"
```

### Gateway (`gateway/config.toml`)

```toml
[consul]
address = "http://127.0.0.1:8500"
poll_interval_secs = 10

[etcd]
endpoints = ["http://127.0.0.1:2379"]
domain_prefix = "/hermes/domains"
cluster_prefix = "/hermes/clusters"

[instance_registry]
enabled = true
prefix = "/hermes/instances"
lease_ttl_secs = 15
```

## Project Structure

```
hermes/
├── gateway/              # Rust data plane
│   ├── src/
│   │   ├── config/       # Configuration types and etcd sync
│   │   ├── discovery/    # Consul service discovery
│   │   ├── etcd/         # etcd client
│   │   ├── metrics/      # Prometheus metrics registry
│   │   ├── middleware/    # Rate limiting
│   │   ├── proxy/        # HTTP proxy handler and filter chain
│   │   ├── routing/      # Radix tree route matching
│   │   ├── server/       # Bootstrap, state, admin API, instance registry
│   │   └── upstream/     # Cluster, load balancing, circuit breaker, health check
│   ├── Cargo.toml
│   └── Makefile
├── server/               # Go control plane + Web UI
│   ├── cmd/server/       # Entry point
│   ├── internal/
│   │   ├── config/       # Config loading
│   │   ├── handler/      # HTTP handlers, auth middleware, OIDC
│   │   ├── model/        # Domain/Cluster data models and validation
│   │   └── store/        # PostgreSQL store, RBAC, scopes
│   ├── web/              # Vue 3 SPA frontend
│   └── Makefile
├── controller/           # Go config sync controller
│   ├── cmd/controller/   # Entry point
│   ├── internal/
│   │   ├── config/       # Config loading
│   │   ├── controller/   # Main loop, reconcile, heartbeat, instance watch
│   │   └── transport/    # HMAC-signed HTTP transport
│   └── Makefile
└── tests/                # End-to-end tests
```

## Tech Stack

| Component | Language | Key Dependencies |
|-----------|----------|-----------------|
| Gateway | Rust | tokio, hyper 1.x, serde, prometheus, arc-swap, dashmap, jemalloc |
| Server | Go | pgx (PostgreSQL), zap (logging) |
| Controller | Go | etcd client v3, zap (logging) |
| Web UI | JavaScript | Vue 3, Vue Router 4, Axios, Vite 5 |

## License

This project is licensed under the [Apache License 2.0](LICENSE).
