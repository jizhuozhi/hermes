package config

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level controller configuration.
type Config struct {
	ControlPlane ControlPlaneConfig `yaml:"controlplane"`
	Etcd         EtcdConfig         `yaml:"etcd"`
	Auth         AuthConfig         `yaml:"auth"`
	Election     ElectionConfig     `yaml:"election"`
}

type ControlPlaneConfig struct {
	URL               string `yaml:"url"`                // e.g. "http://hermes-controlplane:9080"
	PollInterval      int    `yaml:"poll_interval"`      // seconds, for fallback if long-poll fails
	ReconcileInterval int    `yaml:"reconcile_interval"` // seconds, periodic full reconciliation (default 60)
	Namespace         string `yaml:"namespace"`          // namespace to pull config from (default "default")
}

// AuthConfig holds AK/SK for HMAC-SHA256 authentication to the control plane.
// If AccessKey is empty, requests are sent without authentication.
type AuthConfig struct {
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type EtcdConfig struct {
	Endpoints      []string `yaml:"endpoints"`
	DomainPrefix   string   `yaml:"domain_prefix"`   // "/hermes/domains"
	ClusterPrefix  string   `yaml:"cluster_prefix"`  // "/hermes/clusters"
	InstancePrefix string   `yaml:"instance_prefix"` // "/hermes/instances"
	MetaPrefix     string   `yaml:"meta_prefix"`     // "/hermes/meta"
	Username       string   `yaml:"username"`
	Password       string   `yaml:"password"`
}

type ElectionConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Prefix   string `yaml:"prefix"`    // etcd election prefix, default "/hermes/election"
	LeaseTTL int    `yaml:"lease_ttl"` // seconds, default 15
}

// Load reads configuration from a YAML file (if it exists) and applies
// environment variable overrides. When the file does not exist, only
// built-in defaults and environment variables are used — this allows
// the service to start with zero configuration for local development.
func Load(path string) (*Config, error) {
	cfg := &Config{
		ControlPlane: ControlPlaneConfig{
			URL:               "http://127.0.0.1:9080",
			PollInterval:      5,
			ReconcileInterval: 60,
			Namespace:         "default",
		},
		Etcd: EtcdConfig{
			Endpoints:      []string{"http://127.0.0.1:2379"},
			DomainPrefix:   "/hermes/domains",
			ClusterPrefix:  "/hermes/clusters",
			InstancePrefix: "/hermes/instances",
			MetaPrefix:     "/hermes/meta",
		},
		Election: ElectionConfig{
			Prefix:   "/hermes/election",
			LeaseTTL: 15,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	// Environment variable overrides (HERMES_ prefix).
	if v := os.Getenv("HERMES_CONTROLPLANE_URL"); v != "" {
		cfg.ControlPlane.URL = v
	}
	if v := os.Getenv("HERMES_CONTROLPLANE_POLL_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ControlPlane.PollInterval = n
		}
	}
	if v := os.Getenv("HERMES_CONTROLPLANE_RECONCILE_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ControlPlane.ReconcileInterval = n
		}
	}
	if v := os.Getenv("HERMES_CONTROLPLANE_NAMESPACE"); v != "" {
		cfg.ControlPlane.Namespace = v
	}
	if v := os.Getenv("HERMES_ETCD_ENDPOINTS"); v != "" {
		cfg.Etcd.Endpoints = strings.Split(v, ",")
	}
	if v := os.Getenv("HERMES_ETCD_DOMAIN_PREFIX"); v != "" {
		cfg.Etcd.DomainPrefix = v
	}
	if v := os.Getenv("HERMES_ETCD_CLUSTER_PREFIX"); v != "" {
		cfg.Etcd.ClusterPrefix = v
	}
	if v := os.Getenv("HERMES_ETCD_INSTANCE_PREFIX"); v != "" {
		cfg.Etcd.InstancePrefix = v
	}
	if v := os.Getenv("HERMES_ETCD_META_PREFIX"); v != "" {
		cfg.Etcd.MetaPrefix = v
	}
	if v := os.Getenv("HERMES_ETCD_USERNAME"); v != "" {
		cfg.Etcd.Username = v
	}
	if v := os.Getenv("HERMES_ETCD_PASSWORD"); v != "" {
		cfg.Etcd.Password = v
	}
	if v := os.Getenv("HERMES_AUTH_ACCESS_KEY"); v != "" {
		cfg.Auth.AccessKey = v
	}
	if v := os.Getenv("HERMES_AUTH_SECRET_KEY"); v != "" {
		cfg.Auth.SecretKey = v
	}
	if v := os.Getenv("HERMES_ELECTION_ENABLED"); v != "" {
		cfg.Election.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("HERMES_ELECTION_PREFIX"); v != "" {
		cfg.Election.Prefix = v
	}
	if v := os.Getenv("HERMES_ELECTION_LEASE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Election.LeaseTTL = n
		}
	}

	// Ensure namespace always has a value.
	if cfg.ControlPlane.Namespace == "" {
		cfg.ControlPlane.Namespace = "default"
	}
	if cfg.Election.Prefix == "" {
		cfg.Election.Prefix = "/hermes/election"
	}
	if cfg.Election.LeaseTTL <= 0 {
		cfg.Election.LeaseTTL = 15
	}
	return cfg, nil
}
