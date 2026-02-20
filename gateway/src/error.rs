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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn display_no_route_match() {
        assert_eq!(GatewayError::NoRouteMatch.to_string(), "no route matched");
    }

    #[test]
    fn display_no_upstream() {
        assert_eq!(
            GatewayError::NoUpstream.to_string(),
            "no upstream available"
        );
    }

    #[test]
    fn display_rate_limited() {
        assert_eq!(GatewayError::RateLimited.to_string(), "rate limited");
    }

    #[test]
    fn display_upstream_timeout() {
        assert_eq!(
            GatewayError::UpstreamTimeout.to_string(),
            "upstream timeout"
        );
    }

    #[test]
    fn display_upstream_connect() {
        assert_eq!(
            GatewayError::UpstreamConnect("conn refused".to_string()).to_string(),
            "upstream connect error: conn refused"
        );
    }

    #[test]
    fn display_consul() {
        assert_eq!(
            GatewayError::Consul("timeout".to_string()).to_string(),
            "consul error: timeout"
        );
    }

    #[test]
    fn display_config() {
        assert_eq!(
            GatewayError::Config("bad yaml".to_string()).to_string(),
            "config error: bad yaml"
        );
    }

    #[test]
    fn display_internal() {
        assert_eq!(
            GatewayError::Internal("oops".to_string()).to_string(),
            "internal error: oops"
        );
    }
}
