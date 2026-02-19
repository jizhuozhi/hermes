mod admin;
pub mod bootstrap;
pub mod instance_registry;
pub mod runtime;
mod state;

pub use instance_registry::InstanceRegistry;
pub use state::{GatewayState, InfraState};

use crate::proxy;
use anyhow::Result;
use hyper::body::Incoming;
use hyper::service::service_fn;
use hyper::Request;
use hyper_util::rt::{TokioExecutor, TokioIo};
use hyper_util::server::conn::auto;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Arc;
use tokio::net::TcpListener;
use tokio::sync::Notify;
use tracing::{error, info};

/// Run the main proxy server with graceful shutdown support.
///
/// When `shutdown` is notified the server stops accepting new connections and
/// waits up to `DRAIN_TIMEOUT` for in-flight requests to complete before
/// forcibly dropping them.
pub async fn run_proxy_server(
    listen: &str,
    state: GatewayState,
    shutdown: Arc<Notify>,
) -> Result<()> {
    const DRAIN_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);

    let addr: SocketAddr = listen.parse()?;
    let listener = TcpListener::bind(addr).await?;
    info!("server: proxy listening, addr={}", addr);

    // Track in-flight connections so we can drain them on shutdown.
    let in_flight = Arc::new(tokio::sync::Semaphore::new(0));
    // Atomic counter for reading active connection count (metrics crate gauges are write-only).
    let active_conns = Arc::new(AtomicI64::new(0));

    loop {
        let accepted = tokio::select! {
            result = listener.accept() => result,
            _ = shutdown.notified() => {
                info!("server: proxy: stop accepting new connections, draining...");
                break;
            }
        };

        let (stream, peer_addr) = match accepted {
            Ok(v) => {
                metrics::counter!(
                    "gateway_connections_total",
                    "status" => "accepted",
                )
                .increment(1);
                v
            }
            Err(e) => {
                error!("server: proxy: accept failed, error={}", e);
                metrics::counter!(
                    "gateway_connections_total",
                    "status" => "error",
                )
                .increment(1);
                continue;
            }
        };

        metrics::gauge!("gateway_connections_active").increment(1.0);
        active_conns.fetch_add(1, Ordering::Relaxed);

        let state = state.clone();
        // Add a permit for this connection — `close()` will wait on these.
        in_flight.add_permits(1);
        let in_flight = in_flight.clone();
        let active_conns = active_conns.clone();

        tokio::spawn(async move {
            let io = TokioIo::new(stream);
            let state_inner = state.clone();
            let svc = service_fn(move |req: Request<Incoming>| {
                let state = state_inner.clone();
                async move { proxy::handle_request(req, state, peer_addr).await }
            });

            if let Err(e) = auto::Builder::new(TokioExecutor::new())
                .http1()
                .keep_alive(true)
                .http2()
                .keep_alive_interval(Some(std::time::Duration::from_secs(20)))
                .serve_connection_with_upgrades(io, svc)
                .await
            {
                if !e.to_string().contains("connection closed") {
                    error!(
                        "server: proxy: connection error, peer={}, error={}",
                        peer_addr, e
                    );
                }
            }

            metrics::gauge!("gateway_connections_active").decrement(1.0);
            active_conns.fetch_sub(1, Ordering::Relaxed);
            // Consume one permit — signal that this connection is done.
            let _ = in_flight.acquire().await;
        });
    }

    // Drain phase: wait for all in-flight connections to finish (or timeout).
    let active = active_conns.load(Ordering::Relaxed);
    if active > 0 {
        info!(
            "server: proxy: waiting for {} active connections to drain",
            active
        );
        let drain = async {
            loop {
                if active_conns.load(Ordering::Relaxed) == 0 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(100)).await;
            }
        };
        match tokio::time::timeout(DRAIN_TIMEOUT, drain).await {
            Ok(_) => info!("server: proxy: all connections drained"),
            Err(_) => {
                let remaining = active_conns.load(Ordering::Relaxed);
                info!(
                    "server: proxy: drain timeout ({}s), {} connections still active",
                    DRAIN_TIMEOUT.as_secs(),
                    remaining
                );
            }
        }
    }

    Ok(())
}

/// Run a simple admin server for health/readiness checks and metrics.
pub async fn run_admin_server(listen: &str, state: GatewayState) -> Result<()> {
    let addr: SocketAddr = listen.parse()?;
    let listener = TcpListener::bind(addr).await?;
    info!("server: admin listening, addr={}", addr);

    loop {
        let (stream, _) = listener.accept().await?;
        let state = state.clone();

        tokio::spawn(async move {
            let io = TokioIo::new(stream);
            let svc = service_fn(move |req: Request<Incoming>| {
                let state = state.clone();
                async move { admin::handle_admin(req, state) }
            });

            if let Err(e) = auto::Builder::new(TokioExecutor::new())
                .http1()
                .keep_alive(true)
                .serve_connection_with_upgrades(io, svc)
                .await
            {
                if !e.to_string().contains("connection closed") {
                    error!("server: admin: connection error, error={}", e);
                }
            }
        });
    }
}
