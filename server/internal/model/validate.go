package model

import (
	"fmt"
	"strings"
)

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidateConfig validates domains and clusters together.
// Following the nginx model, all routes belong to domains (no independent routes).
func ValidateConfig(cfg *GatewayConfig) []ValidationError {
	var errs []ValidationError

	// Build cluster name set for cross-reference validation.
	clusterNames := make(map[string]bool)
	for _, c := range cfg.Clusters {
		clusterNames[c.Name] = true
	}

	errs = append(errs, ValidateClusters(cfg.Clusters)...)
	errs = append(errs, ValidateDomains(cfg.Domains, clusterNames)...)

	return errs
}

// ValidateDomains validates domain definitions.
func ValidateDomains(domains []DomainConfig, clusterNames map[string]bool) []ValidationError {
	var errs []ValidationError

	seen := make(map[string]bool)
	for i, d := range domains {
		prefix := fmt.Sprintf("domains[%d]", i)

		if d.Name == "" {
			errs = append(errs, ValidationError{prefix + ".name", "required"})
		} else if seen[d.Name] {
			errs = append(errs, ValidationError{prefix + ".name", fmt.Sprintf("duplicate name: %s", d.Name)})
		} else {
			seen[d.Name] = true
		}

		if len(d.Hosts) == 0 {
			errs = append(errs, ValidationError{prefix + ".hosts", "at least one host is required"})
		}
		for j, host := range d.Hosts {
			if host == "" {
				errs = append(errs, ValidationError{
					fmt.Sprintf("%s.hosts[%d]", prefix, j), "empty host",
				})
			}
		}

		routePrefix := fmt.Sprintf("%s.routes", prefix)
		errs = append(errs, ValidateRoutes(d.Routes, clusterNames, routePrefix)...)
	}

	return errs
}

// ValidateDomain validates a single domain config.
func ValidateDomain(d *DomainConfig, clusterNames map[string]bool) []ValidationError {
	return ValidateDomains([]DomainConfig{*d}, clusterNames)
}

// ValidateRoutes validates route definitions.
func ValidateRoutes(routes []RouteConfig, clusterNames map[string]bool, pathPrefix string) []ValidationError {
	var errs []ValidationError

	seen := make(map[string]bool)
	for i, r := range routes {
		prefix := fmt.Sprintf("%s[%d]", pathPrefix, i)

		if r.Name == "" {
			errs = append(errs, ValidationError{prefix + ".name", "required"})
		} else if seen[r.Name] {
			errs = append(errs, ValidationError{prefix + ".name", fmt.Sprintf("duplicate name: %s", r.Name)})
		} else {
			seen[r.Name] = true
		}

		if r.URI == "" {
			errs = append(errs, ValidationError{prefix + ".uri", "required"})
		} else if !strings.HasPrefix(r.URI, "/") {
			errs = append(errs, ValidationError{prefix + ".uri", "must start with /"})
		}

		if len(r.Clusters) == 0 {
			errs = append(errs, ValidationError{prefix + ".clusters", "at least one cluster reference is required"})
		}

		for j, wc := range r.Clusters {
			cp := fmt.Sprintf("%s.clusters[%d]", prefix, j)
			if wc.Name == "" {
				errs = append(errs, ValidationError{cp + ".name", "required"})
			} else if clusterNames != nil && !clusterNames[wc.Name] {
				errs = append(errs, ValidationError{cp + ".name", fmt.Sprintf("cluster %q not found", wc.Name)})
			}
			if wc.Weight < 0 {
				errs = append(errs, ValidationError{cp + ".weight", "must be >= 0"})
			}
		}

		// Validate header matchers
		for j, h := range r.Headers {
			hp := fmt.Sprintf("%s.headers[%d]", prefix, j)
			if h.Name == "" {
				errs = append(errs, ValidationError{hp + ".name", "required"})
			}
			switch h.MatchType {
			case "", "exact", "prefix", "regex", "present":
				// valid
			default:
				errs = append(errs, ValidationError{hp + ".match_type", "must be 'exact', 'prefix', 'regex', or 'present'"})
			}
		}

		// Validate cluster override header
		if r.ClusterOverrideHeader != nil {
			h := *r.ClusterOverrideHeader
			if h == "" {
				errs = append(errs, ValidationError{prefix + ".cluster_override_header", "must be non-empty when set"})
			} else if strings.ContainsAny(h, " \t\n\r") {
				errs = append(errs, ValidationError{prefix + ".cluster_override_header", "must not contain whitespace"})
			}
		}

		// Validate header transforms
		errs = append(errs, validateHeaderTransforms(r.RequestHeaderTransforms, prefix+".request_header_transforms")...)
		errs = append(errs, validateHeaderTransforms(r.ResponseHeaderTransforms, prefix+".response_header_transforms")...)

		if r.RateLimit != nil {
			rl := r.RateLimit
			rlp := prefix + ".rate_limit"
			switch rl.Mode {
			case "req":
				if rl.Rate == nil || *rl.Rate <= 0 {
					errs = append(errs, ValidationError{rlp + ".rate", "required for mode=req and must be > 0"})
				}
				if rl.Burst != nil && *rl.Burst < 0 {
					errs = append(errs, ValidationError{rlp + ".burst", "must be >= 0"})
				}
			case "count":
				if rl.Count == nil || *rl.Count <= 0 {
					errs = append(errs, ValidationError{rlp + ".count", "required for mode=count and must be > 0"})
				}
				if rl.TimeWindow == nil || *rl.TimeWindow <= 0 {
					errs = append(errs, ValidationError{rlp + ".time_window", "required for mode=count and must be > 0"})
				}
			default:
				errs = append(errs, ValidationError{rlp + ".mode", "must be 'req' or 'count'"})
			}
			switch rl.Key {
			case "", "route", "host_uri", "remote_addr", "uri":
				// valid
			default:
				errs = append(errs, ValidationError{rlp + ".key", "must be 'route', 'host_uri', 'remote_addr', or 'uri'"})
			}
			if rl.RejectedCode < 400 || rl.RejectedCode > 599 {
				if rl.RejectedCode != 0 { // 0 means not set, gateway defaults to 429
					errs = append(errs, ValidationError{rlp + ".rejected_code", "must be a 4xx or 5xx HTTP status code"})
				}
			}
		}

		// Validate max_body_bytes
		if r.MaxBodyBytes != nil && *r.MaxBodyBytes < 0 {
			errs = append(errs, ValidationError{prefix + ".max_body_bytes", "must be >= 0"})
		}

		if r.Status != 0 && r.Status != 1 {
			errs = append(errs, ValidationError{prefix + ".status", "must be 0 or 1"})
		}
	}

	return errs
}

// ValidateClusters validates cluster definitions.
func ValidateClusters(clusters []ClusterConfig) []ValidationError {
	var errs []ValidationError

	seen := make(map[string]bool)
	for i, c := range clusters {
		prefix := fmt.Sprintf("clusters[%d]", i)

		if c.Name == "" {
			errs = append(errs, ValidationError{prefix + ".name", "required"})
		} else if seen[c.Name] {
			errs = append(errs, ValidationError{prefix + ".name", fmt.Sprintf("duplicate name: %s", c.Name)})
		} else {
			seen[c.Name] = true
		}

		if c.LBType == "" {
			errs = append(errs, ValidationError{prefix + ".type", "required"})
		}

		switch c.Scheme {
		case "http", "https", "":
			// valid
		default:
			errs = append(errs, ValidationError{prefix + ".scheme", "must be 'http' or 'https'"})
		}

		switch c.PassHost {
		case "pass", "node", "rewrite", "":
			// valid
		default:
			errs = append(errs, ValidationError{prefix + ".pass_host", "must be 'pass', 'node', or 'rewrite'"})
		}

		if c.PassHost == "rewrite" && (c.UpstreamHost == nil || *c.UpstreamHost == "") {
			errs = append(errs, ValidationError{prefix + ".upstream_host", "required when pass_host is 'rewrite'"})
		}

		hasStatic := len(c.Nodes) > 0
		hasDiscovery := c.DiscoveryType != nil && c.ServiceName != nil
		if !hasStatic && !hasDiscovery {
			errs = append(errs, ValidationError{prefix, "must have either static nodes or discovery_type+service_name"})
		}

		if c.Timeout.Connect <= 0 || c.Timeout.Read <= 0 {
			errs = append(errs, ValidationError{prefix + ".timeout", "connect and read must be > 0"})
		}

		// Validate health check
		if c.HealthCheck != nil {
			hcPrefix := prefix + ".health_check"
			if c.HealthCheck.Active != nil {
				ap := hcPrefix + ".active"
				a := c.HealthCheck.Active
				if a.Interval <= 0 {
					errs = append(errs, ValidationError{ap + ".interval", "must be > 0"})
				}
				if a.Path == "" {
					errs = append(errs, ValidationError{ap + ".path", "required"})
				} else if !strings.HasPrefix(a.Path, "/") {
					errs = append(errs, ValidationError{ap + ".path", "must start with /"})
				}
				if a.Timeout <= 0 {
					errs = append(errs, ValidationError{ap + ".timeout", "must be > 0"})
				}
				if a.HealthyThreshold <= 0 {
					errs = append(errs, ValidationError{ap + ".healthy_threshold", "must be > 0"})
				}
				if a.UnhealthyThreshold <= 0 {
					errs = append(errs, ValidationError{ap + ".unhealthy_threshold", "must be > 0"})
				}
				if len(a.HealthyStatuses) == 0 {
					errs = append(errs, ValidationError{ap + ".healthy_statuses", "at least one status code is required"})
				}
				for j, s := range a.HealthyStatuses {
					if s < 100 || s > 599 {
						errs = append(errs, ValidationError{fmt.Sprintf("%s.healthy_statuses[%d]", ap, j), "must be a valid HTTP status code (100-599)"})
					}
				}
				if a.Concurrency < 0 {
					errs = append(errs, ValidationError{ap + ".concurrency", "must be >= 0"})
				}
			}
			if c.HealthCheck.Passive != nil {
				pp := hcPrefix + ".passive"
				p := c.HealthCheck.Passive
				if p.UnhealthyThreshold <= 0 {
					errs = append(errs, ValidationError{pp + ".unhealthy_threshold", "must be > 0"})
				}
				if len(p.UnhealthyStatuses) == 0 {
					errs = append(errs, ValidationError{pp + ".unhealthy_statuses", "at least one status code is required"})
				}
				for j, s := range p.UnhealthyStatuses {
					if s < 100 || s > 599 {
						errs = append(errs, ValidationError{fmt.Sprintf("%s.unhealthy_statuses[%d]", pp, j), "must be a valid HTTP status code (100-599)"})
					}
				}
			}
			if c.HealthCheck.Active == nil && c.HealthCheck.Passive == nil {
				errs = append(errs, ValidationError{hcPrefix, "at least one of active or passive is required"})
			}
		}

		// Validate retry
		if c.Retry != nil {
			rp := prefix + ".retry"
			r := c.Retry
			if r.Count <= 0 {
				errs = append(errs, ValidationError{rp + ".count", "must be > 0"})
			}
			if len(r.RetryOnStatuses) == 0 && !r.RetryOnConnectFailure && !r.RetryOnTimeout {
				errs = append(errs, ValidationError{rp, "at least one retry trigger is required (statuses, connect_failure, or timeout)"})
			}
			for j, s := range r.RetryOnStatuses {
				if s < 100 || s > 599 {
					errs = append(errs, ValidationError{fmt.Sprintf("%s.retry_on_statuses[%d]", rp, j), "must be a valid HTTP status code (100-599)"})
				}
			}
		}

		// Validate circuit breaker
		if c.CircuitBreaker != nil {
			cbp := prefix + ".circuit_breaker"
			cb := c.CircuitBreaker
			if cb.FailureThreshold <= 0 {
				errs = append(errs, ValidationError{cbp + ".failure_threshold", "must be > 0"})
			}
			if cb.SuccessThreshold <= 0 {
				errs = append(errs, ValidationError{cbp + ".success_threshold", "must be > 0"})
			}
			if cb.OpenDurationSecs <= 0 {
				errs = append(errs, ValidationError{cbp + ".open_duration_secs", "must be > 0"})
			}
		}
	}

	return errs
}

// ValidateCluster validates a single cluster config.
func ValidateCluster(c *ClusterConfig) []ValidationError {
	return ValidateClusters([]ClusterConfig{*c})
}

// validateHeaderTransforms validates a list of header transform rules.
func validateHeaderTransforms(transforms []HeaderTransform, pathPrefix string) []ValidationError {
	var errs []ValidationError
	for i, t := range transforms {
		tp := fmt.Sprintf("%s[%d]", pathPrefix, i)
		if t.Name == "" {
			errs = append(errs, ValidationError{tp + ".name", "required"})
		} else if strings.ContainsAny(t.Name, " \t\n\r") {
			errs = append(errs, ValidationError{tp + ".name", "must not contain whitespace"})
		}
		switch t.Action {
		case "set", "add", "remove":
			// valid
		case "":
			errs = append(errs, ValidationError{tp + ".action", "required (set, add, or remove)"})
		default:
			errs = append(errs, ValidationError{tp + ".action", "must be 'set', 'add', or 'remove'"})
		}
		if t.Action != "remove" && t.Value == "" {
			errs = append(errs, ValidationError{tp + ".value", "required for set/add actions"})
		}
	}
	return errs
}
