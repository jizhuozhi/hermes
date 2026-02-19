use crate::config::DomainConfig;
use crate::routing::radix_tree::{self, MatchResult, RadixTree};
use std::collections::HashMap;
use std::sync::atomic::AtomicU32;
use std::sync::Arc;

pub use radix_tree::{ClusterSelector, CompiledRoute};

/// The route table — host-partitioned with a RadixTree per partition.
///
/// Built from `DomainConfig` definitions. Following the nginx model,
/// all routes belong to a domain. The special `_default` domain with
/// `hosts: ["_"]` serves as the catch-all fallback (analogous to
/// nginx `server_name _`).
///
/// Matching order:
/// 1. Exact host match (O(1) HashMap lookup)
/// 2. Wildcard host patterns (linear scan over small set)
/// 3. Default tree (domain with host `_`)
///
/// Within each tree, URI matching follows radix tree priority:
/// exact path > longest prefix wildcard. Method, header, and priority
/// filtering happen at the leaf level.
pub struct RouteTable {
    /// Routes with an exact (non-wildcard) host.
    exact_hosts: HashMap<String, RadixTree>,
    /// Routes with a wildcard host pattern (e.g. `*.example.com`).
    wildcard_hosts: Vec<(String, RadixTree)>,
    /// Default fallback tree — the `_default` domain with host `_`.
    default: RadixTree,
    route_count: usize,
}

impl RouteTable {
    /// Build from domain configs.
    ///
    /// Domains with host `_` contribute to the default fallback tree.
    /// All other hosts are partitioned into exact or wildcard trees.
    pub fn new(domains: &[DomainConfig], instance_count: Option<Arc<AtomicU32>>) -> Self {
        let mut exact_hosts: HashMap<String, Vec<crate::config::RouteConfig>> = HashMap::new();
        let mut wildcard_hosts: HashMap<String, Vec<crate::config::RouteConfig>> = HashMap::new();
        let mut default_routes: Vec<crate::config::RouteConfig> = Vec::new();
        let mut count = 0;

        for domain in domains {
            for cfg in &domain.routes {
                if cfg.status != 1 {
                    continue;
                }
                tracing::debug!(
                    "routing: compiled route entry, domain={}, name={}, uri={}, priority={}, headers={}",
                    domain.name,
                    cfg.name,
                    cfg.uri,
                    cfg.priority,
                    cfg.headers.len(),
                );
                count += 1;

                for host in &domain.hosts {
                    if host == "_" {
                        default_routes.push(cfg.clone());
                    } else if host.contains('*') {
                        wildcard_hosts
                            .entry(host.clone())
                            .or_default()
                            .push(cfg.clone());
                    } else {
                        exact_hosts
                            .entry(host.to_ascii_lowercase())
                            .or_default()
                            .push(cfg.clone());
                    }
                }
            }
        }

        let exact_trees: HashMap<String, RadixTree> = exact_hosts
            .into_iter()
            .map(|(host, cfgs)| {
                let mut tree = RadixTree::new(instance_count.clone());
                for c in cfgs {
                    tree.insert(c);
                }
                (host, tree)
            })
            .collect();

        let wildcard_trees: Vec<(String, RadixTree)> = wildcard_hosts
            .into_iter()
            .map(|(pattern, cfgs)| {
                let mut tree = RadixTree::new(instance_count.clone());
                for c in cfgs {
                    tree.insert(c);
                }
                (pattern, tree)
            })
            .collect();

        let mut default_tree = RadixTree::new(instance_count);
        for c in default_routes {
            default_tree.insert(c);
        }

        tracing::info!("routing: compiled route table, count={}", count);

        Self {
            exact_hosts: exact_trees,
            wildcard_hosts: wildcard_trees,
            default: default_tree,
            route_count: count,
        }
    }

    /// Match a request against the route table.
    ///
    /// Lookup order: exact host → wildcard host → default (host `_`).
    /// Within each partition: exact URI > longest prefix wildcard,
    /// then header filter, method filter, then priority tie-break.
    pub fn match_route(
        &self,
        host: &str,
        uri: &str,
        method: &str,
        headers: &http::HeaderMap,
    ) -> Option<Arc<CompiledRoute>> {
        let method_upper = method.to_uppercase();
        let req_host = host.split(':').next().unwrap_or(host);
        let req_host_lower = req_host.to_ascii_lowercase();

        // 1. Exact host
        if let Some(tree) = self.exact_hosts.get(&req_host_lower) {
            if let Some(route) = match_in_tree(tree, uri, &method_upper, headers) {
                return Some(route);
            }
        }

        // 2. Wildcard host patterns
        for (pattern, tree) in &self.wildcard_hosts {
            if host_matches(req_host, pattern) {
                if let Some(route) = match_in_tree(tree, uri, &method_upper, headers) {
                    return Some(route);
                }
            }
        }

        // 3. Default fallback (host "_")
        match_in_tree(&self.default, uri, &method_upper, headers)
    }

    pub fn route_count(&self) -> usize {
        self.route_count
    }

    pub fn all_routes(&self) -> Vec<&Arc<CompiledRoute>> {
        let mut result = Vec::new();
        for tree in self.exact_hosts.values() {
            result.extend(tree.all_routes());
        }
        for (_, tree) in &self.wildcard_hosts {
            result.extend(tree.all_routes());
        }
        result.extend(self.default.all_routes());
        result
    }
}

/// Match inside a single RadixTree, applying method filter, header filter, and priority.
fn match_in_tree(
    tree: &RadixTree,
    uri: &str,
    method_upper: &str,
    headers: &http::HeaderMap,
) -> Option<Arc<CompiledRoute>> {
    match tree.match_uri(uri) {
        MatchResult::Exact {
            exact,
            wildcard_fallbacks,
        } => best_route_from(exact, method_upper, headers).or_else(|| {
            // Try wildcard fallbacks from deepest to shallowest.
            for wc in &wildcard_fallbacks {
                if let Some(route) = best_route_from(wc, method_upper, headers) {
                    return Some(route);
                }
            }
            None
        }),
        MatchResult::Wildcard(candidates) => {
            // Try wildcard candidates from deepest to shallowest.
            for wc in &candidates {
                if let Some(route) = best_route_from(wc, method_upper, headers) {
                    return Some(route);
                }
            }
            None
        }
        MatchResult::None => None,
    }
}

/// Pick the highest-priority route that matches method + headers.
fn best_route_from(
    routes: &[Arc<CompiledRoute>],
    method_upper: &str,
    headers: &http::HeaderMap,
) -> Option<Arc<CompiledRoute>> {
    let mut best: Option<&Arc<CompiledRoute>> = None;

    for route in routes {
        // Method filter
        if !route.methods.is_empty() && !route.methods.iter().any(|m| m == method_upper) {
            continue;
        }

        // Header filter — all matchers must pass (AND semantics)
        if !route.header_matchers.is_empty() {
            let all_match = route.header_matchers.iter().all(|hm| {
                let header_val = headers.get(&hm.name).and_then(|v| v.to_str().ok());
                hm.matches(header_val)
            });
            if !all_match {
                continue;
            }
        }

        match best {
            Some(current) if route.priority <= current.priority => {}
            _ => best = Some(route),
        }
    }

    best.cloned()
}

/// Match a request host against a route host pattern.
///
/// Supported patterns:
/// - `api.example.com` — exact match (case-insensitive)
/// - `*.example.com` — suffix wildcard (matches any subdomain)
/// - `api.*` — prefix wildcard (matches any TLD/domain change)
fn host_matches(req_host: &str, pattern: &str) -> bool {
    if let Some(suffix) = pattern.strip_prefix('*') {
        req_host.len() >= suffix.len()
            && req_host[req_host.len() - suffix.len()..].eq_ignore_ascii_case(suffix)
    } else if let Some(prefix) = pattern.strip_suffix('*') {
        req_host.len() >= prefix.len() && req_host[..prefix.len()].eq_ignore_ascii_case(prefix)
    } else {
        req_host.eq_ignore_ascii_case(pattern)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{DomainConfig, HeaderMatcher, RouteConfig, WeightedCluster};

    fn make_route(name: &str, uri: &str, priority: i32) -> RouteConfig {
        RouteConfig {
            id: name.to_string(),
            name: name.to_string(),
            uri: uri.to_string(),
            methods: vec![],
            headers: vec![],
            priority,
            clusters: vec![WeightedCluster {
                name: "default".to_string(),
                weight: 100,
            }],
            rate_limit: None,
            cluster_override_header: None,
            request_header_transforms: vec![],
            response_header_transforms: vec![],
            status: 1,
            plugins: None,
            max_body_bytes: None,
            enable_compression: false,
        }
    }

    fn make_domain(name: &str, hosts: Vec<&str>, routes: Vec<RouteConfig>) -> DomainConfig {
        DomainConfig {
            name: name.to_string(),
            hosts: hosts.into_iter().map(|h| h.to_string()).collect(),
            routes,
        }
    }

    /// Helper: create a `_default` domain with host `_` (nginx catch-all).
    fn make_default_domain(routes: Vec<RouteConfig>) -> DomainConfig {
        make_domain("_default", vec!["_"], routes)
    }

    fn empty_headers() -> http::HeaderMap {
        http::HeaderMap::new()
    }

    #[test]
    fn test_specific_route_over_catchall() {
        let domains = vec![make_domain(
            "myapp",
            vec!["api.example.com"],
            vec![
                make_route("catchall", "/*", 0),
                make_route("specific", "/v1/users/profile", 0),
            ],
        )];
        let table = RouteTable::new(&domains, None);
        let matched = table
            .match_route(
                "api.example.com",
                "/v1/users/profile",
                "POST",
                &empty_headers(),
            )
            .unwrap();
        assert_eq!(matched.name, "specific");
    }

    #[test]
    fn test_host_based_routing() {
        let domains = vec![
            make_domain(
                "host-a",
                vec!["a.example.com"],
                vec![make_route("host-a", "/*", 0)],
            ),
            make_domain(
                "host-b",
                vec!["b.example.com"],
                vec![make_route("host-b", "/*", 0)],
            ),
        ];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("a.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "host-a");

        let matched = table
            .match_route("b.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "host-b");
    }

    #[test]
    fn test_multi_host_domain() {
        let domains = vec![make_domain(
            "multi",
            vec!["api.example.com", "api-internal.example.com"],
            vec![make_route("multi-route", "/*", 0)],
        )];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("api.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "multi-route");

        let matched = table
            .match_route("api-internal.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "multi-route");

        assert!(table
            .match_route("other.example.com", "/foo", "GET", &empty_headers())
            .is_none());
    }

    #[test]
    fn test_no_match() {
        let domains = vec![make_domain(
            "host-a",
            vec!["a.example.com"],
            vec![make_route("host-a", "/*", 0)],
        )];
        let table = RouteTable::new(&domains, None);
        assert!(table
            .match_route("unknown.example.com", "/foo", "GET", &empty_headers())
            .is_none());
    }

    #[test]
    fn test_priority_within_same_path() {
        let domains = vec![make_default_domain(vec![
            make_route("low", "/api/*", 0),
            make_route("high", "/api/*", 10),
        ])];
        let table = RouteTable::new(&domains, None);
        let matched = table
            .match_route("any.com", "/api/v1/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "high");
    }

    #[test]
    fn test_exact_over_wildcard() {
        let domains = vec![make_default_domain(vec![
            make_route("wc", "/v1/users/*", 100),
            make_route("exact", "/v1/users/list", 0),
        ])];
        let table = RouteTable::new(&domains, None);
        let matched = table
            .match_route("any.com", "/v1/users/list", "POST", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "exact");
    }

    #[test]
    fn test_deepest_wildcard() {
        let domains = vec![make_default_domain(vec![
            make_route("shallow", "/api/*", 0),
            make_route("deep", "/api/v1/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("any.com", "/api/v1/users", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "deep");

        let matched = table
            .match_route("any.com", "/api/v2/other", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "shallow");
    }

    #[test]
    fn test_method_filtering() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                methods: vec!["POST".to_string()],
                ..make_route("post_only", "/api/v1/submit", 0)
            },
            make_route("catchall", "/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("any.com", "/api/v1/submit", "POST", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "post_only");

        // GET should not match post_only, should fall through to catchall.
        let matched = table
            .match_route("any.com", "/api/v1/submit", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "catchall");
    }

    #[test]
    fn test_disabled_route_excluded() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                status: 0,
                ..make_route("disabled", "/api/v1/submit", 100)
            },
            make_route("catchall", "/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("any.com", "/api/v1/submit", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "catchall");
    }

    #[test]
    fn test_host_wildcard_suffix() {
        let domains = vec![make_domain(
            "myapp",
            vec!["*.example.com"],
            vec![make_route("myapp", "/*", 0)],
        )];
        let table = RouteTable::new(&domains, None);

        assert!(table
            .match_route("api.example.com", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("cdn.example.com", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("other.test.com", "/foo", "GET", &empty_headers())
            .is_none());
    }

    #[test]
    fn test_host_wildcard_prefix() {
        let domains = vec![make_domain(
            "api",
            vec!["api.*"],
            vec![make_route("api", "/*", 0)],
        )];
        let table = RouteTable::new(&domains, None);

        assert!(table
            .match_route("api.example.com", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("api.newbrand.io", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("web.example.com", "/foo", "GET", &empty_headers())
            .is_none());
    }

    #[test]
    fn test_host_exact_over_wildcard() {
        let domains = vec![
            make_domain(
                "wildcard",
                vec!["*.example.com"],
                vec![make_route("wildcard", "/*", 0)],
            ),
            make_domain(
                "exact",
                vec!["api.example.com"],
                vec![make_route("exact", "/*", 10)],
            ),
        ];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("api.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "exact");

        let matched = table
            .match_route("cdn.example.com", "/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "wildcard");
    }

    #[test]
    fn test_default_fallback_after_host() {
        let domains = vec![
            make_domain(
                "host-only",
                vec!["a.example.com"],
                vec![make_route("host-only", "/api/*", 0)],
            ),
            make_default_domain(vec![make_route("default", "/api/*", 0)]),
        ];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("a.example.com", "/api/v1", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "host-only");

        let matched = table
            .match_route("b.example.com", "/api/v1", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "default");
    }

    #[test]
    fn test_method_fallback_to_default() {
        let domains = vec![
            make_domain(
                "host-a",
                vec!["a.example.com"],
                vec![RouteConfig {
                    methods: vec!["POST".to_string()],
                    ..make_route("host_post", "/api/submit", 0)
                }],
            ),
            make_default_domain(vec![make_route("default_catchall", "/*", 0)]),
        ];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("a.example.com", "/api/submit", "POST", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "host_post");

        // GET on same host+uri: host tree has no GET match, falls to default.
        let matched = table
            .match_route("a.example.com", "/api/submit", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "default_catchall");
    }

    #[test]
    fn test_header_exact_match() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                headers: vec![HeaderMatcher {
                    name: "x-api-version".to_string(),
                    value: "v2".to_string(),
                    match_type: "exact".to_string(),
                    invert: false,
                }],
                ..make_route("v2_route", "/api/*", 10)
            },
            make_route("default_route", "/api/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let mut headers = http::HeaderMap::new();
        headers.insert("x-api-version", "v2".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "v2_route");

        let matched = table
            .match_route("any.com", "/api/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "default_route");

        let mut headers = http::HeaderMap::new();
        headers.insert("x-api-version", "v1".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "default_route");
    }

    #[test]
    fn test_header_prefix_match() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                headers: vec![HeaderMatcher {
                    name: "x-tenant".to_string(),
                    value: "corp-".to_string(),
                    match_type: "prefix".to_string(),
                    invert: false,
                }],
                ..make_route("corp_route", "/api/*", 10)
            },
            make_route("default_route", "/api/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let mut headers = http::HeaderMap::new();
        headers.insert("x-tenant", "corp-acme".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "corp_route");

        let mut headers = http::HeaderMap::new();
        headers.insert("x-tenant", "indie-shop".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "default_route");
    }

    #[test]
    fn test_header_present_match() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                headers: vec![HeaderMatcher {
                    name: "x-canary".to_string(),
                    value: String::new(),
                    match_type: "present".to_string(),
                    invert: false,
                }],
                ..make_route("canary_route", "/api/*", 10)
            },
            make_route("default_route", "/api/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let mut headers = http::HeaderMap::new();
        headers.insert("x-canary", "anything".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "canary_route");

        let matched = table
            .match_route("any.com", "/api/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "default_route");
    }

    #[test]
    fn test_header_invert_match() {
        let domains = vec![make_default_domain(vec![
            RouteConfig {
                headers: vec![HeaderMatcher {
                    name: "x-internal".to_string(),
                    value: String::new(),
                    match_type: "present".to_string(),
                    invert: true,
                }],
                ..make_route("external_route", "/api/*", 10)
            },
            make_route("catchall", "/api/*", 0),
        ])];
        let table = RouteTable::new(&domains, None);

        let matched = table
            .match_route("any.com", "/api/foo", "GET", &empty_headers())
            .unwrap();
        assert_eq!(matched.name, "external_route");

        let mut headers = http::HeaderMap::new();
        headers.insert("x-internal", "true".parse().unwrap());
        let matched = table
            .match_route("any.com", "/api/foo", "GET", &headers)
            .unwrap();
        assert_eq!(matched.name, "catchall");
    }

    #[test]
    fn test_domain_with_mixed_hosts() {
        let domains = vec![make_domain(
            "mixed",
            vec!["api.example.com", "*.staging.example.com"],
            vec![make_route("mixed-route", "/*", 0)],
        )];
        let table = RouteTable::new(&domains, None);

        assert!(table
            .match_route("api.example.com", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("app.staging.example.com", "/foo", "GET", &empty_headers())
            .is_some());
        assert!(table
            .match_route("other.example.com", "/foo", "GET", &empty_headers())
            .is_none());
    }
}
