use crate::config::types::CircuitBreakerConfig;
use crate::proxy::context::{full_body, BoxBody, RequestContext};
use crate::proxy::filter::{Filter, FilterResult};
use crate::routing::{CompiledRoute, HeaderOp, HeaderOpAction};
use crate::server::GatewayState;
use crate::upstream::{BreakerCheck, Cluster, RequestGuard, UpstreamTarget};
use bytes::Bytes;
use http::header::{
    ACCEPT_ENCODING, CONNECTION, CONTENT_ENCODING, CONTENT_LENGTH, HOST, TRANSFER_ENCODING,
};
use http::{HeaderName, HeaderValue, StatusCode};
use http_body_util::BodyExt;
use http_body_util::StreamBody;
use hyper::body::{Frame, Incoming};
use hyper::Request;
use hyper::Response;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Instant;
use tracing::{debug, warn};

/// Handle an incoming HTTP request through a phased lifecycle:
///
/// 1. ROUTE_MATCH  — route matching
/// 2. ON_REQUEST   — filter chain (rate limit, ip restriction, ...)
/// 3. CLUSTER_SELECT — weighted cluster selection
/// 4. UPSTREAM     — select upstream node from cluster, build & send request (with retry)
/// 5. ON_RESPONSE  — filter chain (cors headers, compression, ...)
/// 6. LOG          — finalize metrics
pub async fn handle_request(
    req: Request<Incoming>,
    state: GatewayState,
    peer_addr: SocketAddr,
) -> Result<Response<BoxBody>, hyper::Error> {
    let host = req
        .headers()
        .get(HOST)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let uri_path = req.uri().path().to_string();
    let method = req.method().as_str().to_string();
    let mut req_headers = req.headers().clone();

    // Determine the real client IP: trust existing X-Forwarded-For left-most
    // entry if present (assumes a trusted reverse proxy in front), otherwise
    // fall back to the TCP peer address.
    let client_ip = req_headers
        .get("x-forwarded-for")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.split(',').next())
        .and_then(|s| s.trim().parse::<std::net::IpAddr>().ok())
        .unwrap_or_else(|| peer_addr.ip());

    // Inject / append standard X-Forwarded-* headers for the upstream.
    inject_forwarded_headers(&mut req_headers, peer_addr, &host);

    let mut ctx = RequestContext::new(host, uri_path, method, client_ip);

    // Route match
    let route = match phase_route_match(&ctx, &req_headers, &state) {
        Ok(r) => r,
        Err(resp) => return Ok(resp),
    };

    ctx.route_name = route.name.clone();
    ctx.route = Some(route.clone());

    metrics::gauge!(
        "gateway_http_requests_in_flight",
        "route" => ctx.route_name.clone(),
    )
    .increment(1.0);

    // Request filters
    let filters = &route.filters;

    if let Some(resp) = phase_on_request(filters, &mut ctx).await {
        return Ok(resp);
    }

    // Body size check: reject early if Content-Length exceeds max_body_bytes.
    if let Some(max_bytes) = route.max_body_bytes {
        if let Some(cl) = req_headers
            .get(CONTENT_LENGTH)
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.parse::<u64>().ok())
        {
            if cl > max_bytes {
                debug!(
                    "proxy: request body too large, content_length={}, max={}, route={}",
                    cl, max_bytes, ctx.route_name
                );
                return Ok(ctx.error_response(StatusCode::PAYLOAD_TOO_LARGE, "payload too large"));
            }
        }
    }

    let selection = match select_weighted_cluster(&route, &req_headers, &state) {
        Some(s) => s,
        None => {
            warn!("proxy: no cluster resolved, route={}", ctx.route_name);
            return Ok(ctx.error_response(StatusCode::SERVICE_UNAVAILABLE, "service unavailable"));
        }
    };
    let cluster_overridden = selection.overridden;
    let cluster = selection.cluster;

    // Apply request-phase header transforms before upstream.
    apply_header_transforms(&route.request_header_ops, &mut req_headers);

    // Capture client's Accept-Encoding before forwarding (for response compression).
    let accept_encoding = req_headers
        .get(ACCEPT_ENCODING)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_owned());

    // Upstream proxy (using selected cluster)
    let (upstream_resp, upstream_elapsed) =
        match phase_upstream(req, &mut ctx, &route, &cluster, &req_headers).await {
            Ok(result) => result,
            Err(resp) => return Ok(resp),
        };

    // Response filters
    let mut final_resp = build_downstream_response(upstream_resp);

    // Inject diagnostic headers only when the request used a cluster override
    // header, so internal cluster names are not leaked to normal callers.
    if cluster_overridden {
        if let Ok(v) = HeaderValue::from_str(cluster.name()) {
            final_resp
                .headers_mut()
                .insert(HeaderName::from_static("x-hermes-cluster"), v);
        }
        final_resp.headers_mut().insert(
            HeaderName::from_static("x-hermes-cluster-override"),
            HeaderValue::from_static("true"),
        );
    }

    // Apply response-phase header transforms.
    apply_header_transforms(&route.response_header_ops, final_resp.headers_mut());

    phase_on_response(filters, &ctx, &mut final_resp);

    // Response compression (gzip / brotli) — streaming, route-level control.
    // Only compress if the route has compression enabled, upstream didn't
    // already encode, and the client accepts a supported encoding.
    if route.enable_compression {
        let already_encoded = final_resp.headers().contains_key(CONTENT_ENCODING);
        if !already_encoded {
            if let Some(ref ae) = accept_encoding {
                final_resp = try_compress_response(final_resp, ae);
            }
        }
    }

    // Logging
    phase_log(&ctx, &final_resp, upstream_elapsed, &cluster);

    Ok(final_resp)
}

#[allow(clippy::result_large_err)]
fn phase_route_match(
    ctx: &RequestContext,
    req_headers: &http::HeaderMap,
    state: &GatewayState,
) -> Result<Arc<CompiledRoute>, Response<BoxBody>> {
    let route_table = state.routing.route_table.load();
    match route_table.match_route(&ctx.host, &ctx.uri_path, &ctx.method, req_headers) {
        Some(r) => Ok(r),
        None => {
            debug!(
                "proxy: no route matched, host={}, uri={}",
                ctx.host, ctx.uri_path
            );
            metrics::counter!(
                "gateway_http_requests_total",
                "route" => "_no_route",
                "method" => ctx.method.clone(),
                "status_code" => "404",
                "upstream_addr" => "",
            )
            .increment(1);
            Err(Response::builder()
                .status(StatusCode::NOT_FOUND)
                .header("content-type", "application/json")
                .body(full_body(r#"{"error":"not found"}"#))
                .unwrap())
        }
    }
}

async fn phase_on_request(
    filters: &[Filter],
    ctx: &mut RequestContext,
) -> Option<Response<BoxBody>> {
    for filter in filters {
        if let FilterResult::Reject(resp) = filter.on_request(ctx).await {
            return Some(resp);
        }
    }
    None
}

/// Result of cluster selection — carries the cluster and whether it was
/// selected via a header override so that response headers can be injected.
struct ClusterSelection {
    cluster: Cluster,
    /// `true` when the cluster was chosen via `cluster_override_header`.
    overridden: bool,
}

/// Select a cluster from the route's weighted cluster list.
/// If the route has `cluster_override_header` set and the request carries
/// that header, use the header value as the cluster name directly.
fn select_weighted_cluster(
    route: &CompiledRoute,
    req_headers: &http::HeaderMap,
    state: &GatewayState,
) -> Option<ClusterSelection> {
    // Check for header-based cluster override.
    if let Some(ref header_name) = route.cluster_override_header {
        if let Some(override_val) = req_headers
            .get(header_name.as_str())
            .and_then(|v| v.to_str().ok())
        {
            if let Some(cluster) = state.upstream.get(override_val) {
                debug!(
                    "proxy: cluster override via header '{}' → cluster '{}'",
                    header_name, override_val
                );
                metrics::counter!(
                    "gateway_cluster_override_total",
                    "route" => route.name.clone(),
                    "cluster" => override_val.to_owned(),
                )
                .increment(1);
                return Some(ClusterSelection {
                    cluster,
                    overridden: true,
                });
            }
            warn!(
                "proxy: cluster override header '{}' requested cluster '{}' but it does not exist, falling back to weighted selection",
                header_name, override_val
            );
        }
    }

    let name = route.cluster_selector.select()?;
    state.upstream.get(name).map(|cluster| ClusterSelection {
        cluster,
        overridden: false,
    })
}

/// Upstream phase: node selection + request forwarding with two-level retry.
async fn phase_upstream(
    req: Request<Incoming>,
    ctx: &mut RequestContext,
    route: &CompiledRoute,
    cluster: &Cluster,
    transformed_headers: &http::HeaderMap,
) -> Result<(Response<Incoming>, std::time::Duration), Response<BoxBody>> {
    let cfg = cluster.config();
    let retry_cfg = cfg.retry.as_ref();
    let cb_cfg = cfg.circuit_breaker.as_ref();
    let max_retries = retry_cfg.map(|r| r.count).unwrap_or(0);

    let node_count = cluster.node_count();

    let mut tried_addrs: Vec<String> = Vec::new();
    let mut last_error: Option<Response<BoxBody>> = None;

    let req_method = req.method().clone();
    let req_uri_pq: String = req
        .uri()
        .path_and_query()
        .map(|pq| pq.as_str().to_owned())
        .unwrap_or_else(|| "/".to_owned());
    // Use transformed headers (request-phase header_transforms already applied).
    let req_headers = transformed_headers.clone();
    let (_, body) = req.into_parts();

    let max_body_bytes = route.max_body_bytes;

    // When retries are enabled, buffer the body so it can be replayed.
    // When retries are disabled (max_retries == 0), stream directly — zero copy.
    //
    // Note on max_body_bytes enforcement for streaming (no-retry) path:
    // Only Content-Length-based check applies (done above). Chunked requests
    // without Content-Length are forwarded as-is — buffering the entire body
    // just for a size check would defeat the purpose of zero-copy streaming.
    // Applications that require strict body size enforcement should set
    // Content-Length or handle it at the application layer.
    let (body_bytes, mut streaming_body): (Option<Bytes>, Option<BoxBody>) = if max_retries > 0 {
        let bytes = match body.collect().await {
            Ok(collected) => collected.to_bytes(),
            Err(e) => {
                warn!(
                    "proxy: failed to read request body, route={}, error={}",
                    ctx.route_name, e
                );
                return Err(ctx.error_response(StatusCode::BAD_REQUEST, "bad request"));
            }
        };
        // Enforce body size limit on buffered body (catches chunked/no-Content-Length).
        if let Some(max) = max_body_bytes {
            if bytes.len() as u64 > max {
                debug!(
                    "proxy: buffered body too large, size={}, max={}, route={}",
                    bytes.len(),
                    max,
                    ctx.route_name
                );
                return Err(ctx.error_response(StatusCode::PAYLOAD_TOO_LARGE, "payload too large"));
            }
        }
        (Some(bytes), None)
    } else {
        (None, Some(body.boxed()))
    };

    // Pre-allocate a reusable buffer for upstream URI construction.
    // Avoids a `format!()` heap allocation inside the retry loop.
    let mut upstream_uri_buf = String::with_capacity(target_uri_capacity(&req_uri_pq));

    // Timeout durations: send = connect + write, read = wait for first response byte + body.
    let send_timeout = std::time::Duration::from_secs_f64(cfg.timeout.send);
    let read_timeout = std::time::Duration::from_secs_f64(cfg.timeout.read);
    // Global deadline: all attempts (initial + retries) share one wall-clock budget.
    // This prevents retries from multiplying the total latency beyond the configured timeout.
    let total_budget = send_timeout + read_timeout;
    let deadline = Instant::now() + total_budget;

    for attempt in 0..=max_retries {
        // Check whether there is meaningful time left before starting a new attempt.
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            warn!(
                "proxy: deadline exhausted before attempt {}, route={}",
                attempt, ctx.route_name
            );
            return Err(last_error.unwrap_or_else(|| {
                ctx.error_response(StatusCode::GATEWAY_TIMEOUT, "gateway timeout")
            }));
        }

        let (target, mut guard, upstream_addr) =
            match select_healthy_node(cluster, ctx, &tried_addrs, cb_cfg, node_count) {
                Some(v) => v,
                None => {
                    warn!("proxy: no upstream available, route={}", ctx.route_name);
                    return Err(last_error.unwrap_or_else(|| {
                        ctx.error_response(StatusCode::SERVICE_UNAVAILABLE, "service unavailable")
                    }));
                }
            };

        ctx.upstream_addr.clear();
        ctx.upstream_addr.push_str(&upstream_addr);

        // Build upstream URI: "{scheme}://{addr}{path_and_query}"
        upstream_uri_buf.clear();
        upstream_uri_buf.push_str(&target.scheme);
        upstream_uri_buf.push_str("://");
        upstream_uri_buf.push_str(&upstream_addr);
        upstream_uri_buf.push_str(&req_uri_pq);

        let mut headers = req_headers.clone();
        apply_host_header(&mut headers, &target, &upstream_addr);
        remove_hop_headers(&mut headers);

        let mut builder = Request::builder()
            .method(req_method.clone())
            .uri(&upstream_uri_buf);
        for (name, value) in &headers {
            builder = builder.header(name, value);
        }

        // Buffered path: clone from cached bytes; streaming path: take once.
        let req_body: BoxBody = if let Some(ref bytes) = body_bytes {
            full_body(bytes.clone())
        } else {
            streaming_body
                .take()
                .unwrap_or_else(crate::proxy::context::empty_body)
        };

        let upstream_req = match builder.body(req_body) {
            Ok(r) => r,
            Err(e) => {
                warn!(
                    "proxy: failed to build upstream request, route={}, error={}",
                    ctx.route_name, e
                );
                return Err(
                    ctx.error_response(StatusCode::INTERNAL_SERVER_ERROR, "internal server error")
                );
            }
        };

        let client = cluster.http_client();

        let upstream_start = Instant::now();
        if attempt == 0 {
            ctx.upstream_start = Some(upstream_start);
        }

        // Per-attempt timeout: capped by the remaining global deadline so that
        // retries cannot extend the total wall-clock beyond the configured budget.
        let per_attempt_timeout = remaining;

        let result = tokio::time::timeout(per_attempt_timeout, client.request(upstream_req)).await;

        match result {
            Ok(Ok(resp)) => {
                let upstream_elapsed = upstream_start.elapsed();
                let status = resp.status().as_u16();

                if let Some(cb) = cb_cfg {
                    if is_server_error(status) {
                        cluster
                            .circuit_breakers()
                            .record_failure(&upstream_addr, cb);
                        guard.mark_failed();
                    } else {
                        cluster
                            .circuit_breakers()
                            .record_success(&upstream_addr, cb);
                    }
                }

                if attempt < max_retries {
                    if let Some(rcfg) = retry_cfg {
                        if rcfg.retry_on_statuses.contains(&status) {
                            debug!(
                                "proxy: retryable status {}, route={}, upstream={}, attempt={}/{}",
                                status,
                                ctx.route_name,
                                upstream_addr,
                                attempt + 1,
                                max_retries
                            );
                            metrics::counter!(
                                "gateway_upstream_retries_total",
                                "route" => ctx.route_name.clone(),
                                "reason" => "status",
                            )
                            .increment(1);
                            guard.mark_failed();
                            tried_addrs.push(upstream_addr);
                            last_error = Some(ctx.error_response(
                                StatusCode::from_u16(status).unwrap_or(StatusCode::BAD_GATEWAY),
                                "bad gateway",
                            ));
                            continue;
                        }
                    }
                }

                drop(guard);
                return Ok((resp, upstream_elapsed));
            }
            Ok(Err(e)) => {
                cluster.record_health_failure(&upstream_addr);
                if let Some(cb) = cb_cfg {
                    cluster
                        .circuit_breakers()
                        .record_failure(&upstream_addr, cb);
                }
                guard.mark_failed();

                let can_retry = retry_cfg
                    .map(|r| r.retry_on_connect_failure)
                    .unwrap_or(false)
                    && attempt < max_retries;

                if can_retry {
                    debug!(
                        "proxy: connect error (retrying), route={}, upstream={}, attempt={}/{}, error={}",
                        ctx.route_name, upstream_addr, attempt + 1, max_retries, e
                    );
                    metrics::counter!(
                        "gateway_upstream_retries_total",
                        "route" => ctx.route_name.clone(),
                        "reason" => "connect_error",
                    )
                    .increment(1);
                    tried_addrs.push(upstream_addr);
                    last_error = Some(ctx.error_response(StatusCode::BAD_GATEWAY, "bad gateway"));
                    continue;
                }

                warn!(
                    "proxy: upstream error, route={}, upstream={}, error={}",
                    ctx.route_name, upstream_uri_buf, e
                );
                return Err(ctx.error_response(StatusCode::BAD_GATEWAY, "bad gateway"));
            }
            Err(_) => {
                cluster.record_health_failure(&upstream_addr);
                if let Some(cb) = cb_cfg {
                    cluster
                        .circuit_breakers()
                        .record_failure(&upstream_addr, cb);
                }
                guard.mark_failed();

                let can_retry =
                    retry_cfg.map(|r| r.retry_on_timeout).unwrap_or(false) && attempt < max_retries;

                if can_retry {
                    debug!(
                        "proxy: timeout (retrying), route={}, upstream={}, attempt={}/{}",
                        ctx.route_name,
                        upstream_addr,
                        attempt + 1,
                        max_retries
                    );
                    metrics::counter!(
                        "gateway_upstream_retries_total",
                        "route" => ctx.route_name.clone(),
                        "reason" => "timeout",
                    )
                    .increment(1);
                    tried_addrs.push(upstream_addr);
                    last_error =
                        Some(ctx.error_response(StatusCode::GATEWAY_TIMEOUT, "gateway timeout"));
                    continue;
                }

                warn!(
                    "proxy: upstream timeout, route={}, upstream={}",
                    ctx.route_name, upstream_uri_buf
                );
                return Err(ctx.error_response(StatusCode::GATEWAY_TIMEOUT, "gateway timeout"));
            }
        }
    }

    Err(last_error.unwrap_or_else(|| ctx.error_response(StatusCode::BAD_GATEWAY, "bad gateway")))
}

/// Estimate capacity needed for the upstream URI buffer.
#[inline]
fn target_uri_capacity(path_and_query: &str) -> usize {
    // "https://".len() == 8, typical addr ~21 chars
    30 + path_and_query.len()
}

/// Level 1 selection: pick a node via cluster's LB, skipping unhealthy/breaker-rejected.
fn select_healthy_node(
    cluster: &Cluster,
    ctx: &RequestContext,
    tried_addrs: &[String],
    cb_cfg: Option<&CircuitBreakerConfig>,
    max_skip: usize,
) -> Option<(UpstreamTarget, RequestGuard, String)> {
    for _ in 0..=max_skip {
        let (target, guard) = cluster.select_upstream()?;

        // endpoint() returns &str (zero-alloc); we only allocate an owned
        // String when returning the successful candidate.
        let addr = target.instance.endpoint().to_owned();

        if tried_addrs.iter().any(|a| a == &addr) {
            continue;
        }

        // Skip nodes marked unhealthy by active health checks.
        if !cluster.is_node_healthy(&addr) {
            debug!(
                "proxy: node unhealthy (active hc), skipping upstream={}, route={}",
                addr, ctx.route_name
            );
            continue;
        }

        if let Some(cb) = cb_cfg {
            match cluster.circuit_breakers().check(&addr, cb) {
                BreakerCheck::Allowed | BreakerCheck::Probe => {}
                BreakerCheck::Rejected => {
                    debug!(
                        "proxy: circuit breaker open, skipping upstream={}, route={}",
                        addr, ctx.route_name
                    );
                    metrics::counter!(
                        "gateway_circuit_breaker_rejected_total",
                        "route" => ctx.route_name.clone(),
                        "upstream_addr" => addr.clone(),
                    )
                    .increment(1);
                    continue;
                }
            }
        }

        return Some((target, guard, addr));
    }

    None
}

fn apply_host_header(headers: &mut http::HeaderMap, target: &UpstreamTarget, upstream_addr: &str) {
    match &*target.pass_host {
        "node" => {
            headers.insert(
                HOST,
                HeaderValue::from_str(upstream_addr)
                    .unwrap_or_else(|_| HeaderValue::from_static("")),
            );
        }
        "rewrite" => {
            if let Some(ref uh) = target.upstream_host {
                headers.insert(
                    HOST,
                    HeaderValue::from_str(uh).unwrap_or_else(|_| HeaderValue::from_static("")),
                );
            }
        }
        _ => {}
    }
}

fn is_server_error(status: u16) -> bool {
    (500..600).contains(&status)
}

fn phase_on_response(filters: &[Filter], ctx: &RequestContext, resp: &mut Response<BoxBody>) {
    for filter in filters.iter().rev() {
        filter.on_response(ctx, resp);
    }
}

fn phase_log(
    ctx: &RequestContext,
    resp: &Response<BoxBody>,
    upstream_elapsed: std::time::Duration,
    cluster: &Cluster,
) {
    let resp_status = resp.status().as_u16();

    if let Some(hc) = &cluster.config().health_check {
        if let Some(passive) = &hc.passive {
            if passive.unhealthy_statuses.contains(&resp_status) {
                cluster.record_health_failure(&ctx.upstream_addr);
            } else {
                cluster.record_health_success(&ctx.upstream_addr);
            }
        }
    }

    if let Some(cl) = resp
        .headers()
        .get(CONTENT_LENGTH)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<f64>().ok())
    {
        metrics::histogram!(
            "gateway_http_response_size_bytes",
            "route" => ctx.route_name.clone(),
            "upstream_addr" => ctx.upstream_addr.clone(),
        )
        .record(cl);
    }

    ctx.finalize_metrics(resp_status);

    // Structured access log — one line per request at info level.
    let total_ms = ctx.start.elapsed().as_millis();
    let upstream_ms = upstream_elapsed.as_millis();

    tracing::info!(
        client_ip = %ctx.client_ip,
        method = %ctx.method,
        host = %ctx.host,
        path = %ctx.uri_path,
        status = resp_status,
        route = %ctx.route_name,
        upstream = %ctx.upstream_addr,
        latency_ms = %total_ms,
        upstream_ms = %upstream_ms,
        "access"
    );
}

/// Apply pre-compiled header transform ops to a HeaderMap.
/// Used for both request-phase (upstream coloring) and response-phase transforms.
#[inline]
fn apply_header_transforms(ops: &[HeaderOp], headers: &mut http::HeaderMap) {
    for op in ops {
        match op.action {
            HeaderOpAction::Set => {
                headers.insert(op.name.clone(), op.value.clone());
            }
            HeaderOpAction::Add => {
                headers.append(op.name.clone(), op.value.clone());
            }
            HeaderOpAction::Remove => {
                headers.remove(&op.name);
            }
        }
    }
}

fn build_downstream_response(upstream_resp: Response<Incoming>) -> Response<BoxBody> {
    let (parts, body) = upstream_resp.into_parts();
    let mut builder = Response::builder().status(parts.status);
    for (name, value) in &parts.headers {
        builder = builder.header(name, value);
    }
    builder.body(body.boxed()).unwrap()
}

fn remove_hop_headers(headers: &mut http::HeaderMap) {
    let hop_headers: &[HeaderName] = &[
        CONNECTION,
        HeaderName::from_static("keep-alive"),
        HeaderName::from_static("proxy-authenticate"),
        HeaderName::from_static("proxy-authorization"),
        HeaderName::from_static("te"),
        HeaderName::from_static("trailers"),
        TRANSFER_ENCODING,
        HeaderName::from_static("upgrade"),
    ];

    for h in hop_headers {
        headers.remove(h);
    }
}

/// Inject standard `X-Forwarded-*` and `X-Real-IP` headers so upstream
/// services can identify the original client and protocol.
///
/// Behavior:
/// - `X-Forwarded-For`: append the TCP peer IP to any existing value
///   (comma-separated list per RFC 7239 semantics).
/// - `X-Forwarded-Proto`: set to `https` if the request arrived over TLS,
///   otherwise `http`. The gateway does not terminate TLS — a front ALB
///   is expected to handle TLS and set this header before traffic arrives.
/// - `X-Forwarded-Host`: set to the original `Host` header value.
/// - `X-Real-IP`: set to the TCP peer IP (always overwritten — represents
///   the immediate downstream hop).
fn inject_forwarded_headers(
    headers: &mut http::HeaderMap,
    peer_addr: SocketAddr,
    original_host: &str,
) {
    static XFF: HeaderName = HeaderName::from_static("x-forwarded-for");
    static XFP: HeaderName = HeaderName::from_static("x-forwarded-proto");
    static XFH: HeaderName = HeaderName::from_static("x-forwarded-host");
    static XRI: HeaderName = HeaderName::from_static("x-real-ip");

    let peer_ip = peer_addr.ip().to_string();

    // X-Forwarded-For: append peer IP
    if let Some(existing) = headers.get(&XFF).and_then(|v| v.to_str().ok()) {
        let mut combined = String::with_capacity(existing.len() + 2 + peer_ip.len());
        combined.push_str(existing);
        combined.push_str(", ");
        combined.push_str(&peer_ip);
        if let Ok(v) = HeaderValue::from_str(&combined) {
            headers.insert(XFF.clone(), v);
        }
    } else if let Ok(v) = HeaderValue::from_str(&peer_ip) {
        headers.insert(XFF.clone(), v);
    }

    // X-Forwarded-Proto: trust the incoming value (e.g. set by ALB after TLS
    // termination), only default to "http" when absent.
    if !headers.contains_key(&XFP) {
        headers.insert(XFP.clone(), HeaderValue::from_static("http"));
    }

    // X-Forwarded-Host
    if !original_host.is_empty() {
        if let Ok(v) = HeaderValue::from_str(original_host) {
            headers.insert(XFH.clone(), v);
        }
    }

    // X-Real-IP: always the immediate peer
    if let Ok(v) = HeaderValue::from_str(&peer_ip) {
        headers.insert(XRI.clone(), v);
    }
}

/// Negotiate the best encoding from the client's `Accept-Encoding` header.
/// Returns `"br"` (brotli) or `"gzip"` if accepted (q > 0), otherwise `None`.
/// Properly parses quality values: `gzip;q=1, br;q=0` will NOT select br.
fn negotiate_encoding(accept_encoding: &str) -> Option<&'static str> {
    let mut br_ok = false;
    let mut gzip_ok = false;

    for part in accept_encoding.split(',') {
        let part = part.trim();
        let mut tokens = part.splitn(2, ';');
        let encoding = tokens.next().unwrap_or("").trim().to_ascii_lowercase();

        // Parse quality value (defaults to 1.0 if not specified).
        let q: f32 = tokens
            .next()
            .and_then(|params| {
                params.split(';').find_map(|p| {
                    let p = p.trim();
                    p.strip_prefix("q=")
                        .and_then(|v| v.trim().parse::<f32>().ok())
                })
            })
            .unwrap_or(1.0);

        if q <= 0.0 {
            continue;
        }

        match encoding.as_str() {
            "br" => br_ok = true,
            "gzip" => gzip_ok = true,
            "*" => {
                br_ok = true;
                gzip_ok = true;
            }
            _ => {}
        }
    }

    // Prefer brotli over gzip when both are accepted.
    if br_ok {
        Some("br")
    } else if gzip_ok {
        Some("gzip")
    } else {
        None
    }
}

/// Attempt to compress the response body using streaming compression.
///
/// This implementation wraps the response body stream with an async
/// compression encoder (gzip or brotli) so data is compressed on-the-fly
/// as chunks are read. This preserves streaming semantics — no need to
/// buffer the entire body in memory first.
///
/// Compression is controlled at the route level (`enable_compression`).
/// The caller is responsible for checking that compression is enabled
/// and that the upstream hasn't already set `Content-Encoding`.
fn try_compress_response(resp: Response<BoxBody>, accept_encoding: &str) -> Response<BoxBody> {
    // Negotiate encoding.
    let encoding = match negotiate_encoding(accept_encoding) {
        Some(e) => e,
        None => return resp,
    };

    let version = resp.version();
    let (mut parts, body) = resp.into_parts();

    // Convert the body into an AsyncBufRead stream, then wrap with the
    // appropriate compression encoder. The result is a streaming body
    // that compresses chunks on the fly.
    let body_reader = tokio_util::io::StreamReader::new(BodyStream(body));
    let buf_reader = tokio::io::BufReader::new(body_reader);

    let compressed_body: BoxBody = match encoding {
        "gzip" => {
            let encoder = async_compression::tokio::bufread::GzipEncoder::new(buf_reader);
            wrap_encoder_as_body(encoder)
        }
        "br" => {
            let encoder = async_compression::tokio::bufread::BrotliEncoder::with_quality(
                buf_reader,
                async_compression::Level::Fastest,
            );
            wrap_encoder_as_body(encoder)
        }
        _ => unreachable!(),
    };

    // Update headers for compressed response.
    parts
        .headers
        .insert(CONTENT_ENCODING, HeaderValue::from_static(encoding));
    // Remove Content-Length — compressed size is unknown for streaming.
    parts.headers.remove(CONTENT_LENGTH);
    // HTTP/1.x: set chunked transfer encoding since we don't know final size.
    // HTTP/2+ does not use Transfer-Encoding (framing is handled by the protocol).
    if version == http::Version::HTTP_11 || version == http::Version::HTTP_10 {
        parts
            .headers
            .insert(TRANSFER_ENCODING, HeaderValue::from_static("chunked"));
    }

    Response::from_parts(parts, compressed_body)
}

/// Wrap an `AsyncRead` compression encoder into a `BoxBody`.
///
/// Reads chunks from the encoder and yields them as HTTP body frames.
/// The encoder is pinned on the heap to satisfy `Unpin` requirements.
fn wrap_encoder_as_body<R>(encoder: R) -> BoxBody
where
    R: tokio::io::AsyncRead + Send + Sync + 'static,
{
    use tokio::io::AsyncReadExt;

    let encoder = Box::pin(encoder);
    let stream = futures_util::stream::unfold(encoder, |mut enc| async move {
        let mut buf = vec![0u8; 8192];
        match enc.read(&mut buf).await {
            Ok(0) => None, // EOF
            Ok(n) => {
                buf.truncate(n);
                let frame: Result<Frame<Bytes>, hyper::Error> = Ok(Frame::data(Bytes::from(buf)));
                Some((frame, enc))
            }
            Err(_) => None, // On error, end the stream gracefully.
        }
    });
    BodyExt::boxed(StreamBody::new(stream))
}

/// Adapter that converts a `BoxBody` into a `Stream<Item = io::Result<Bytes>>`
/// suitable for `tokio_util::io::StreamReader`.
///
/// This is the bridge between hyper's body framing and tokio's I/O traits,
/// enabling async-compression encoders to consume the response body as a
/// byte stream.
struct BodyStream(BoxBody);

impl futures_util::Stream for BodyStream {
    type Item = std::io::Result<Bytes>;

    fn poll_next(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        use hyper::body::Body;

        loop {
            match std::pin::Pin::new(&mut self.0).poll_frame(cx) {
                std::task::Poll::Ready(Some(Ok(frame))) => {
                    if let Ok(data) = frame.into_data() {
                        return std::task::Poll::Ready(Some(Ok(data)));
                    }
                    // Skip non-data frames (trailers, etc.)
                    continue;
                }
                std::task::Poll::Ready(Some(Err(e))) => {
                    return std::task::Poll::Ready(Some(Err(std::io::Error::other(e.to_string()))));
                }
                std::task::Poll::Ready(None) => return std::task::Poll::Ready(None),
                std::task::Poll::Pending => return std::task::Poll::Pending,
            }
        }
    }
}
