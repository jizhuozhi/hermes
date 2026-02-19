use crate::config::{HeaderTransform, RouteConfig, WeightedCluster};
use crate::proxy::filter::{build_route_filters, Filter};
use std::collections::HashMap;
use std::sync::atomic::AtomicU32;
use std::sync::Arc;

/// Pre-computed weighted cluster selector.
///
/// Binds `clusters` and their GCD-normalized prefix-sum array together so
/// they cannot drift out of sync. Provides O(log n) cluster selection.
#[derive(Debug)]
pub struct ClusterSelector {
    clusters: Vec<WeightedCluster>,
    prefix_weights: Vec<u64>,
}

impl ClusterSelector {
    pub fn new(clusters: Vec<WeightedCluster>) -> Self {
        let prefix_weights = build_prefix_weights(&clusters);
        Self {
            clusters,
            prefix_weights,
        }
    }

    /// Select a cluster name proportional to configured weights.
    /// Returns `None` if there are no clusters or all weights are zero.
    pub fn select(&self) -> Option<&str> {
        if self.clusters.is_empty() || self.prefix_weights.is_empty() {
            return None;
        }

        if self.clusters.len() == 1 {
            return Some(&self.clusters[0].name);
        }

        let total = *self.prefix_weights.last().unwrap();
        if total == 0 {
            return None;
        }

        let r = rand::random::<u64>() % total;
        let idx = self.prefix_weights.partition_point(|&cum| cum <= r);
        Some(&self.clusters[idx].name)
    }

    pub fn clusters(&self) -> &[WeightedCluster] {
        &self.clusters
    }
}

/// A compiled header matcher — pre-compiled for fast matching at request time.
#[derive(Debug)]
pub struct CompiledHeaderMatcher {
    pub name: String,
    pub value: String,
    pub match_type: HeaderMatchType,
    pub invert: bool,
    pub regex: Option<regex::Regex>,
}

#[derive(Debug, Clone, Copy)]
pub enum HeaderMatchType {
    Exact,
    Prefix,
    Regex,
    Present,
}

impl CompiledHeaderMatcher {
    pub fn matches(&self, header_value: Option<&str>) -> bool {
        let raw_match = match self.match_type {
            HeaderMatchType::Present => header_value.is_some(),
            HeaderMatchType::Exact => header_value.map_or(false, |v| v == self.value),
            HeaderMatchType::Prefix => header_value.map_or(false, |v| v.starts_with(&self.value)),
            HeaderMatchType::Regex => {
                if let Some(ref re) = self.regex {
                    header_value.map_or(false, |v| re.is_match(v))
                } else {
                    false
                }
            }
        };
        if self.invert { !raw_match } else { raw_match }
    }
}

/// A pre-compiled header transform operation for O(1) dispatch at request time.
#[derive(Debug)]
pub struct HeaderOp {
    pub name: http::HeaderName,
    pub value: http::HeaderValue,
    pub action: HeaderOpAction,
}

#[derive(Debug, Clone, Copy)]
pub enum HeaderOpAction {
    Set,
    Add,
    Remove,
}

/// A compiled route — the runtime domain object.
///
/// Contains only the fields needed at request time. The original
/// `RouteConfig` DTO is consumed during compilation and not retained.
#[derive(Debug)]
pub struct CompiledRoute {
    pub name: String,
    pub uri: String,
    pub priority: i32,
    pub methods: Vec<String>,
    pub header_matchers: Vec<CompiledHeaderMatcher>,
    pub filters: Arc<Vec<Filter>>,
    pub cluster_selector: ClusterSelector,
    /// When set, requests carrying this header bypass weighted selection
    /// and use the header value as the target cluster name.
    pub cluster_override_header: Option<String>,
    /// Pre-compiled request-phase header transforms.
    pub request_header_ops: Vec<HeaderOp>,
    /// Pre-compiled response-phase header transforms.
    pub response_header_ops: Vec<HeaderOp>,
    /// Maximum allowed request body size in bytes (`None` = unlimited).
    pub max_body_bytes: Option<u64>,
    /// Whether response compression is enabled for this route.
    pub enable_compression: bool,
}

/// A node in the compressed radix tree. Each node represents one or more
/// URI segments (e.g. "/v1/users" stored as segments ["v1", "users"]).
#[derive(Debug)]
struct Node {
    segments: Vec<String>,
    children: HashMap<String, Node>,
    exact_routes: Vec<Arc<CompiledRoute>>,
    wildcard_routes: Vec<Arc<CompiledRoute>>,
}

impl Node {
    fn new(segments: Vec<String>) -> Self {
        Self {
            segments,
            children: HashMap::new(),
            exact_routes: Vec::new(),
            wildcard_routes: Vec::new(),
        }
    }
}

/// Compressed radix tree for URI routing, organized by segment boundaries.
///
/// Matching priority at each level:
/// 1. Exact match on the full path
/// 2. Longest prefix match (descend deeper into children)
/// 3. Wildcard match (`/*` suffix) — deepest wildcard wins
#[derive(Debug)]
pub struct RadixTree {
    /// Root node represents "/".
    root: Node,
    /// Shared instance count for distributed rate limiting.
    /// Passed through to `build_route_filters` during insert.
    instance_count: Option<Arc<AtomicU32>>,
}

impl RadixTree {
    pub fn new(instance_count: Option<Arc<AtomicU32>>) -> Self {
        Self {
            root: Node::new(vec![]),
            instance_count,
        }
    }

    /// Insert a route into the tree.
    ///
    /// URI patterns:
    /// - `/v1/users/list` — exact match
    /// - `/v1/users/*` — prefix wildcard (matches /v1/users and everything below)
    /// - `/*` — catch-all
    pub fn insert(&mut self, mut config: RouteConfig) {
        let uri = config.uri.clone();
        let methods: Vec<String> = config.methods.iter().map(|m| m.to_uppercase()).collect();
        let filters = Arc::new(build_route_filters(&config, self.instance_count.clone()));
        let header_matchers = match compile_header_matchers(&config) {
            Ok(hm) => hm,
            Err(e) => {
                tracing::error!(
                    route = %config.name,
                    uri = %config.uri,
                    "route dropped due to invalid header matcher: {e}"
                );
                return;
            }
        };
        let cluster_selector = ClusterSelector::new(std::mem::take(&mut config.clusters));
        let cluster_override_header = config.cluster_override_header.take();
        let request_header_ops = compile_header_ops(&config.request_header_transforms, &config.name);
        let response_header_ops = compile_header_ops(&config.response_header_transforms, &config.name);
        let compiled = Arc::new(CompiledRoute {
            name: config.name,
            uri: config.uri,
            priority: config.priority,
            methods,
            header_matchers,
            filters,
            cluster_selector,
            cluster_override_header,
            request_header_ops,
            response_header_ops,
            max_body_bytes: config.max_body_bytes,
            enable_compression: config.enable_compression,
        });

        let (segments, is_wildcard) = parse_uri_segments(&uri);

        insert_recursive(&mut self.root, &segments, 0, compiled, is_wildcard);
    }

    /// Match a request URI against the tree.
    /// Returns all candidate routes at the best matching level.
    /// Caller should filter by host and method.
    ///
    /// Priority: exact match > longest prefix child > wildcard (deepest first).
    pub fn match_uri<'a>(&'a self, uri: &str) -> MatchResult<'a> {
        let segments = split_uri_segments(uri);
        let mut wildcard_stack: Vec<&[Arc<CompiledRoute>]> = Vec::new();

        match_recursive(&self.root, &segments, 0, &mut wildcard_stack)
    }

    /// Collect all routes in the tree (for debug/metrics).
    pub fn all_routes(&self) -> Vec<&Arc<CompiledRoute>> {
        let mut result = Vec::new();
        collect_routes(&self.root, &mut result);
        result
    }
}

/// Result of a URI match against the tree.
pub enum MatchResult<'a> {
    Exact {
        exact: &'a [Arc<CompiledRoute>],
        /// All wildcard candidates from deepest to shallowest.
        wildcard_fallbacks: Vec<&'a [Arc<CompiledRoute>]>,
    },
    /// All wildcard candidates from deepest to shallowest.
    Wildcard(Vec<&'a [Arc<CompiledRoute>]>),
    None,
}

/// Build GCD-normalized prefix-sum array for weighted cluster selection.
fn build_prefix_weights(clusters: &[WeightedCluster]) -> Vec<u64> {
    if clusters.is_empty() {
        return vec![];
    }

    let weights: Vec<u64> = clusters.iter().map(|c| c.weight as u64).collect();

    // GCD normalization.
    let g = weights.iter().copied().fold(0u64, gcd);
    let divisor = if g == 0 { 1 } else { g };

    let mut prefix = Vec::with_capacity(weights.len());
    let mut cumulative = 0u64;
    for w in &weights {
        cumulative += w / divisor;
        prefix.push(cumulative);
    }
    prefix
}

fn gcd(a: u64, b: u64) -> u64 {
    if b == 0 { a } else { gcd(b, a % b) }
}

/// Parse a URI pattern into segments and whether it's a wildcard.
/// "/v1/users/*" -> (["v1", "users"], true)
/// "/v1/users/list" -> (["v1", "users", "list"], false)
/// "/*" -> ([], true)
fn parse_uri_segments(uri: &str) -> (Vec<String>, bool) {
    let trimmed = uri.trim_start_matches('/');
    if trimmed.is_empty() || trimmed == "*" {
        return (vec![], trimmed == "*");
    }

    let parts: Vec<&str> = trimmed.split('/').collect();
    if let Some(last) = parts.last() {
        if *last == "*" {
            let segs = parts[..parts.len() - 1]
                .iter()
                .map(|s| s.to_string())
                .collect();
            return (segs, true);
        }
    }

    let segs = parts.iter().map(|s| s.to_string()).collect();
    (segs, false)
}

/// Split a request URI into segments.
/// "/v1/users/list" -> ["v1", "users", "list"]
/// "/" -> []
fn split_uri_segments(uri: &str) -> Vec<&str> {
    let path = uri.split('?').next().unwrap_or(uri);
    let trimmed = path.trim_start_matches('/');
    if trimmed.is_empty() {
        return vec![];
    }
    trimmed.split('/').collect()
}

fn insert_recursive(
    node: &mut Node,
    segments: &[String],
    offset: usize,
    route: Arc<CompiledRoute>,
    is_wildcard: bool,
) {
    let remaining = &segments[offset..];

    // No more segments to consume — attach route here.
    if remaining.is_empty() {
        if is_wildcard {
            node.wildcard_routes.push(route);
        } else {
            node.exact_routes.push(route);
        }
        return;
    }

    let first = &remaining[0];

    if let Some(child) = node.children.get_mut(first.as_str()) {
        // Find common prefix length between child.segments and remaining.
        let common = common_prefix_len(&child.segments, remaining);

        if common == child.segments.len() {
            // Child segments fully matched — descend.
            insert_recursive(child, segments, offset + common, route, is_wildcard);
        } else {
            // Partial match — need to split the child node.
            split_and_insert(child, common, segments, offset, route, is_wildcard);
        }
    } else {
        // No matching child — create a new one with all remaining segments compressed.
        let mut new_node = Node::new(remaining.to_vec());
        if is_wildcard {
            new_node.wildcard_routes.push(route);
        } else {
            new_node.exact_routes.push(route);
        }
        node.children.insert(first.clone(), new_node);
    }
}

/// Split an existing child node at the given prefix length, then insert the new route.
fn split_and_insert(
    child: &mut Node,
    common_len: usize,
    segments: &[String],
    offset: usize,
    route: Arc<CompiledRoute>,
    is_wildcard: bool,
) {
    // The child currently represents segments[0..child.segments.len()].
    // We need to split it into:
    //   - A new intermediate node with segments[0..common_len]
    //   - The old child becomes a child of intermediate with segments[common_len..]

    let old_suffix: Vec<String> = child.segments[common_len..].to_vec();
    let old_children = std::mem::take(&mut child.children);
    let old_exact = std::mem::take(&mut child.exact_routes);
    let old_wildcard = std::mem::take(&mut child.wildcard_routes);

    // Create a node for the old suffix.
    let mut old_node = Node::new(old_suffix.clone());
    old_node.children = old_children;
    old_node.exact_routes = old_exact;
    old_node.wildcard_routes = old_wildcard;

    // Truncate the current child to the common prefix.
    child.segments.truncate(common_len);
    child.children.clear();
    child.exact_routes.clear();
    child.wildcard_routes.clear();

    // Re-insert the old node as a child of the (now truncated) child.
    let old_first = old_suffix[0].clone();
    child.children.insert(old_first, old_node);

    // Now insert the new route.
    let new_remaining = &segments[offset + common_len..];
    if new_remaining.is_empty() {
        if is_wildcard {
            child.wildcard_routes.push(route);
        } else {
            child.exact_routes.push(route);
        }
    } else {
        let new_first = new_remaining[0].clone();
        if let Some(existing) = child.children.get_mut(&new_first) {
            let new_offset = offset + common_len;
            insert_recursive(existing, segments, new_offset, route, is_wildcard);
        } else {
            let mut new_node = Node::new(new_remaining.to_vec());
            if is_wildcard {
                new_node.wildcard_routes.push(route);
            } else {
                new_node.exact_routes.push(route);
            }
            child.children.insert(new_first, new_node);
        }
    }
}

fn common_prefix_len(a: &[String], b: &[String]) -> usize {
    a.iter().zip(b.iter()).take_while(|(x, y)| x == y).count()
}

/// Recursive matching: descend the tree, collecting all wildcard candidates (deepest first on return).
fn match_recursive<'a>(
    node: &'a Node,
    segments: &[&str],
    offset: usize,
    wildcard_stack: &mut Vec<&'a [Arc<CompiledRoute>]>,
) -> MatchResult<'a> {
    // If this node has wildcard routes, push onto the stack.
    if !node.wildcard_routes.is_empty() {
        wildcard_stack.push(&node.wildcard_routes);
    }

    let remaining = &segments[offset..];

    if remaining.is_empty() {
        // We've consumed all segments.
        if !node.exact_routes.is_empty() {
            // Return wildcards deepest-first (reverse the stack).
            let mut fallbacks: Vec<&[Arc<CompiledRoute>]> = wildcard_stack.clone();
            fallbacks.reverse();
            return MatchResult::Exact {
                exact: &node.exact_routes,
                wildcard_fallbacks: fallbacks,
            };
        }
        // Fall back to wildcards.
        if wildcard_stack.is_empty() {
            return MatchResult::None;
        }
        let mut candidates: Vec<&[Arc<CompiledRoute>]> = wildcard_stack.clone();
        candidates.reverse();
        return MatchResult::Wildcard(candidates);
    }

    let first = &remaining[0];

    if let Some(child) = node.children.get(*first) {
        // Check if child's compressed segments match.
        let child_len = child.segments.len();
        if remaining.len() >= child_len {
            let matches = child
                .segments
                .iter()
                .zip(remaining.iter())
                .all(|(a, b)| a == b);
            if matches {
                return match_recursive(child, segments, offset + child_len, wildcard_stack);
            }
        }
    }

    // No child matched — return wildcards if any.
    if wildcard_stack.is_empty() {
        return MatchResult::None;
    }
    let mut candidates: Vec<&[Arc<CompiledRoute>]> = wildcard_stack.clone();
    candidates.reverse();
    MatchResult::Wildcard(candidates)
}

fn collect_routes<'a>(node: &'a Node, result: &mut Vec<&'a Arc<CompiledRoute>>) {
    for r in &node.exact_routes {
        result.push(r);
    }
    for r in &node.wildcard_routes {
        result.push(r);
    }
    for child in node.children.values() {
        collect_routes(child, result);
    }
}

fn compile_header_matchers(config: &RouteConfig) -> Result<Vec<CompiledHeaderMatcher>, String> {
    config
        .headers
        .iter()
        .map(|h| {
            let match_type = match h.match_type.as_str() {
                "prefix" => HeaderMatchType::Prefix,
                "regex" => HeaderMatchType::Regex,
                "present" => HeaderMatchType::Present,
                _ => HeaderMatchType::Exact,
            };
            let regex = if matches!(match_type, HeaderMatchType::Regex) {
                Some(regex::Regex::new(&h.value).map_err(|e| {
                    format!(
                        "header '{}' has invalid regex '{}': {}",
                        h.name, h.value, e
                    )
                })?)
            } else {
                None
            };
            Ok(CompiledHeaderMatcher {
                name: h.name.to_ascii_lowercase(),
                value: h.value.clone(),
                match_type,
                invert: h.invert,
                regex,
            })
        })
        .collect()
}

/// Pre-compile header transform rules into `HeaderOp` for zero-parse dispatch.
fn compile_header_ops(transforms: &[HeaderTransform], route_name: &str) -> Vec<HeaderOp> {
    transforms
        .iter()
        .filter_map(|t| {
            let name = match http::HeaderName::from_bytes(t.name.as_bytes()) {
                Ok(n) => n,
                Err(e) => {
                    tracing::error!(
                        route = %route_name,
                        header = %t.name,
                        "invalid header name in transform, skipping: {e}"
                    );
                    return None;
                }
            };
            let action = match t.action.as_str() {
                "add" => HeaderOpAction::Add,
                "remove" => HeaderOpAction::Remove,
                _ => HeaderOpAction::Set,
            };
            let value = match http::HeaderValue::from_str(&t.value) {
                Ok(v) => v,
                Err(e) => {
                    if !matches!(action, HeaderOpAction::Remove) {
                        tracing::error!(
                            route = %route_name,
                            header = %t.name,
                            "invalid header value in transform, skipping: {e}"
                        );
                        return None;
                    }
                    http::HeaderValue::from_static("")
                }
            };
            Some(HeaderOp { name, value, action })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{RouteConfig, WeightedCluster};

    fn make_route(name: &str, uri: &str) -> RouteConfig {
        RouteConfig {
            id: name.to_string(),
            name: name.to_string(),
            uri: uri.to_string(),
            methods: vec![],
            headers: vec![],
            priority: 0,
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

    #[test]
    fn test_exact_match() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("r1", "/v1/users/list"));
        tree.insert(make_route("r2", "/v1/users/create"));

        match tree.match_uri("/v1/users/list") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact.len(), 1);
                assert_eq!(exact[0].name, "r1");
            }
            _ => panic!("expected exact match"),
        }

        match tree.match_uri("/v1/users/create") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact.len(), 1);
                assert_eq!(exact[0].name, "r2");
            }
            _ => panic!("expected exact match"),
        }
    }

    #[test]
    fn test_wildcard_match() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("wc", "/v1/users/*"));

        match tree.match_uri("/v1/users/list") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "wc");
            }
            _ => panic!("expected wildcard match"),
        }

        match tree.match_uri("/v1/users/list/extra") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "wc");
            }
            _ => panic!("expected wildcard match"),
        }
    }

    #[test]
    fn test_exact_over_wildcard() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("wc", "/v1/users/*"));
        tree.insert(make_route("exact", "/v1/users/list"));

        match tree.match_uri("/v1/users/list") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact[0].name, "exact");
            }
            _ => panic!("expected exact match over wildcard"),
        }

        // Other paths under /v1/users/ still match wildcard.
        match tree.match_uri("/v1/users/create") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "wc");
            }
            _ => panic!("expected wildcard match"),
        }
    }

    #[test]
    fn test_deepest_wildcard_wins() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("shallow", "/v1/*"));
        tree.insert(make_route("deep", "/v1/users/*"));

        match tree.match_uri("/v1/users/list") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "deep");
            }
            _ => panic!("expected deepest wildcard"),
        }

        // Path not under /v1/users should match shallow wildcard.
        match tree.match_uri("/v1/other/path") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "shallow");
            }
            _ => panic!("expected shallow wildcard"),
        }
    }

    #[test]
    fn test_catchall() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("catchall", "/*"));
        tree.insert(make_route("specific", "/v1/users/list"));

        match tree.match_uri("/v1/users/list") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact[0].name, "specific");
            }
            _ => panic!("expected exact match"),
        }

        match tree.match_uri("/anything/else") {
            MatchResult::Wildcard(candidates) => {
                assert_eq!(candidates[0][0].name, "catchall");
            }
            _ => panic!("expected catchall wildcard"),
        }
    }

    #[test]
    fn test_no_match() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("r1", "/v1/users/list"));

        match tree.match_uri("/v2/other") {
            MatchResult::None => {}
            _ => panic!("expected no match"),
        }
    }

    #[test]
    fn test_root_exact() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("root", "/"));

        match tree.match_uri("/") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact[0].name, "root");
            }
            _ => panic!("expected root exact match"),
        }
    }

    #[test]
    fn test_query_string_ignored() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("r1", "/v1/items"));

        match tree.match_uri("/v1/items?foo=bar") {
            MatchResult::Exact { exact, .. } => {
                assert_eq!(exact[0].name, "r1");
            }
            _ => panic!("expected exact match ignoring query string"),
        }
    }

    #[test]
    fn test_node_splitting() {
        let mut tree = RadixTree::new(None);
        // Insert two routes that share a common prefix but diverge.
        tree.insert(make_route("abc", "/a/b/c"));
        tree.insert(make_route("abd", "/a/b/d"));

        match tree.match_uri("/a/b/c") {
            MatchResult::Exact { exact, .. } => assert_eq!(exact[0].name, "abc"),
            _ => panic!("expected exact match for /a/b/c"),
        }

        match tree.match_uri("/a/b/d") {
            MatchResult::Exact { exact, .. } => assert_eq!(exact[0].name, "abd"),
            _ => panic!("expected exact match for /a/b/d"),
        }

        // No match for /a/b
        match tree.match_uri("/a/b") {
            MatchResult::None => {}
            _ => panic!("expected no match for /a/b"),
        }
    }

    #[test]
    fn test_wildcard_at_intermediate_and_leaf() {
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("api_all", "/api/*"));
        tree.insert(make_route("api_v1_all", "/api/v1/*"));
        tree.insert(make_route("api_v1_users", "/api/v1/users"));

        // Exact match
        match tree.match_uri("/api/v1/users") {
            MatchResult::Exact { exact, .. } => assert_eq!(exact[0].name, "api_v1_users"),
            _ => panic!("expected exact"),
        }

        // Deeper wildcard
        match tree.match_uri("/api/v1/posts") {
            MatchResult::Wildcard(candidates) => assert_eq!(candidates[0][0].name, "api_v1_all"),
            _ => panic!("expected api_v1_all wildcard"),
        }

        // Shallower wildcard
        match tree.match_uri("/api/v2/anything") {
            MatchResult::Wildcard(candidates) => assert_eq!(candidates[0][0].name, "api_all"),
            _ => panic!("expected api_all wildcard"),
        }
    }

    #[test]
    fn test_wildcard_matches_node_itself() {
        // /v1/* should match /v1 itself (not just /v1/something).
        let mut tree = RadixTree::new(None);
        tree.insert(make_route("v1_all", "/v1/*"));

        match tree.match_uri("/v1") {
            MatchResult::Wildcard(candidates) => assert_eq!(candidates[0][0].name, "v1_all"),
            _ => panic!("expected wildcard match for /v1 with /v1/*"),
        }

        match tree.match_uri("/v1/") {
            MatchResult::Wildcard(candidates) => assert_eq!(candidates[0][0].name, "v1_all"),
            _ => panic!("expected wildcard match for /v1/"),
        }
    }
}
