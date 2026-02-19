use super::GatewayState;
use bytes::Bytes;
use http_body_util::{BodyExt, Full};
use hyper::body::Incoming;
use hyper::{Request, Response};

type BoxBody = http_body_util::combinators::BoxBody<Bytes, hyper::Error>;

fn full_body(data: impl Into<Bytes>) -> BoxBody {
    Full::new(data.into())
        .map_err(|never| match never {})
        .boxed()
}

pub fn handle_admin(
    req: Request<Incoming>,
    state: GatewayState,
) -> Result<Response<BoxBody>, hyper::Error> {
    match req.uri().path() {
        "/health" | "/healthz" => Ok(Response::builder()
            .status(200)
            .body(full_body(r#"{"status":"ok"}"#))
            .unwrap()),

        "/ready" | "/readyz" => {
            let cfg = state.config.load();
            let route_count = cfg.total_route_count();
            Ok(Response::builder()
                .status(200)
                .body(full_body(format!(
                    r#"{{"status":"ready","domains":{},"total_routes":{}}}"#,
                    cfg.domains.len(),
                    route_count,
                )))
                .unwrap())
        }

        "/metrics" => {
            let body = state.metrics.render();
            Ok(Response::builder()
                .status(200)
                .header("content-type", "text/plain; version=0.0.4; charset=utf-8")
                .body(full_body(body))
                .unwrap())
        }

        "/domains" => {
            let cfg = state.config.load();
            let domains: Vec<serde_json::Value> = cfg
                .domains
                .iter()
                .map(|d| {
                    serde_json::json!({
                        "name": d.name,
                        "hosts": d.hosts,
                        "routes": d.routes.iter().map(|r| {
                            serde_json::json!({
                                "name": r.name,
                                "uri": r.uri,
                                "methods": r.methods,
                                "headers": r.headers.iter().map(|h| {
                                    serde_json::json!({
                                        "name": h.name,
                                        "value": h.value,
                                        "match_type": h.match_type,
                                        "invert": h.invert,
                                    })
                                }).collect::<Vec<_>>(),
                                "priority": r.priority,
                                "clusters": r.clusters.iter().map(|c| {
                                    serde_json::json!({"name": c.name, "weight": c.weight})
                                }).collect::<Vec<_>>(),
                            })
                        }).collect::<Vec<_>>(),
                    })
                })
                .collect();

            let body = serde_json::to_string_pretty(&domains).unwrap_or_default();
            Ok(Response::builder()
                .status(200)
                .header("content-type", "application/json")
                .body(full_body(body))
                .unwrap())
        }

        "/routes" => {
            let table = state.routing.route_table.load();
            let routes: Vec<serde_json::Value> = table
                .all_routes()
                .iter()
                .map(|r| {
                    serde_json::json!({
                        "name": r.name,
                        "uri": r.uri,
                        "priority": r.priority,
                        "clusters": r.cluster_selector.clusters().iter().map(|c| {
                            serde_json::json!({"name": c.name, "weight": c.weight})
                        }).collect::<Vec<_>>(),
                    })
                })
                .collect();

            let body = serde_json::to_string_pretty(&routes).unwrap_or_default();
            Ok(Response::builder()
                .status(200)
                .header("content-type", "application/json")
                .body(full_body(body))
                .unwrap())
        }

        _ => Ok(Response::builder()
            .status(404)
            .body(full_body(r#"{"error":"not found"}"#))
            .unwrap()),
    }
}
