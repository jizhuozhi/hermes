use std::fmt;

#[derive(Debug)]
#[allow(dead_code)]
pub enum GatewayError {
    NoRouteMatch,
    NoUpstream,
    RateLimited,
    UpstreamTimeout,
    UpstreamConnect(String),
    Http(reqwest::Error),
    Consul(String),
    Config(String),
    Internal(String),
}

impl fmt::Display for GatewayError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            GatewayError::NoRouteMatch => write!(f, "no route matched"),
            GatewayError::NoUpstream => write!(f, "no upstream available"),
            GatewayError::RateLimited => write!(f, "rate limited"),
            GatewayError::UpstreamTimeout => write!(f, "upstream timeout"),
            GatewayError::UpstreamConnect(msg) => write!(f, "upstream connect error: {}", msg),
            GatewayError::Http(e) => write!(f, "http error: {}", e),
            GatewayError::Consul(msg) => write!(f, "consul error: {}", msg),
            GatewayError::Config(msg) => write!(f, "config error: {}", msg),
            GatewayError::Internal(msg) => write!(f, "internal error: {}", msg),
        }
    }
}

impl std::error::Error for GatewayError {}
