package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Postgres PostgresConfig `yaml:"postgres"`
	Grafana  GrafanaConfig  `yaml:"grafana"`
	OIDC     OIDCConfig     `yaml:"oidc"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"`
}

type PostgresConfig struct {
	DSN string `yaml:"dsn"`
}

type GrafanaConfig struct {
	Dashboards []GrafanaDashboard `yaml:"dashboards"`
}

type GrafanaDashboard struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// OIDCConfig holds OpenID Connect configuration.
// All fields can be overridden by environment variables (OIDC_*).
type OIDCConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Issuer       string `yaml:"issuer"`        // e.g. https://keycloak.example.com/realms/myrealm
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// InitialAdminUsers is a comma-separated list of OIDC usernames or emails.
	// When these users log in for the FIRST TIME, they are automatically granted super-admin.
	// Subsequent logins never change admin status — it's fully managed via the UI.
	// Can also be set via HERMES_INITIAL_ADMIN_USERS env var.
	InitialAdminUsers string `yaml:"initial_admin_users"`
}

// Load reads configuration from a YAML file (if it exists) and applies
// environment variable overrides. When the file does not exist, only
// built-in defaults and environment variables are used — this allows
// the service to start with zero configuration for local development.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{Listen: "0.0.0.0:9080"},
		Postgres: PostgresConfig{
			DSN: "postgres://localhost:5432/hermes?sslmode=disable",
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
	if v := os.Getenv("HERMES_LISTEN"); v != "" {
		cfg.Server.Listen = v
	}
	if v := os.Getenv("HERMES_POSTGRES_DSN"); v != "" {
		cfg.Postgres.DSN = v
	}

	// OIDC overrides (kept backward-compatible with existing env var names).
	if v := os.Getenv("OIDC_ENABLED"); v == "true" || v == "1" {
		cfg.OIDC.Enabled = true
	}
	if v := os.Getenv("OIDC_ISSUER"); v != "" {
		cfg.OIDC.Issuer = v
	}
	if v := os.Getenv("OIDC_CLIENT_ID"); v != "" {
		cfg.OIDC.ClientID = v
	}
	if v := os.Getenv("OIDC_CLIENT_SECRET"); v != "" {
		cfg.OIDC.ClientSecret = v
	}
	if v := os.Getenv("HERMES_INITIAL_ADMIN_USERS"); v != "" {
		cfg.OIDC.InitialAdminUsers = v
	}

	return cfg, nil
}
