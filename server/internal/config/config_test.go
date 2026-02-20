package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load("/tmp/hermes_nonexistent_server_config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:9080", cfg.Server.Listen)
	assert.Equal(t, "postgres://localhost:5432/hermes?sslmode=disable", cfg.Postgres.DSN)
	assert.False(t, cfg.OIDC.Enabled)
	assert.Empty(t, cfg.OIDC.Issuer)
	assert.Empty(t, cfg.AuthMode)
}

func TestLoad_YAMLFile(t *testing.T) {
	yaml := `
server:
  listen: "0.0.0.0:8080"
postgres:
  dsn: "postgres://prod:5432/hermes"
oidc:
  enabled: true
  issuer: "https://keycloak.example.com/realms/myrealm"
  client_id: "hermes"
  client_secret: "secret123"
  initial_admin_users: "admin@example.com"
auth_mode: "oidc"
builtin_auth:
  initial_admin_email: "admin@local"
  initial_admin_password: "pass123"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(yaml), 0644))

	cfg, err := Load(tmp)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:8080", cfg.Server.Listen)
	assert.Equal(t, "postgres://prod:5432/hermes", cfg.Postgres.DSN)
	assert.True(t, cfg.OIDC.Enabled)
	assert.Equal(t, "https://keycloak.example.com/realms/myrealm", cfg.OIDC.Issuer)
	assert.Equal(t, "hermes", cfg.OIDC.ClientID)
	assert.Equal(t, "secret123", cfg.OIDC.ClientSecret)
	assert.Equal(t, "admin@example.com", cfg.OIDC.InitialAdminUsers)
	assert.Equal(t, "oidc", cfg.AuthMode)
	assert.Equal(t, "admin@local", cfg.BuiltinAuth.InitialAdminEmail)
	assert.Equal(t, "pass123", cfg.BuiltinAuth.InitialAdminPassword)
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(":::not yaml"), 0644))

	_, err := Load(tmp)
	assert.Error(t, err)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("HERMES_LISTEN", "0.0.0.0:7070")
	t.Setenv("HERMES_POSTGRES_DSN", "postgres://env:5432/hermes")
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER", "https://env-issuer")
	t.Setenv("OIDC_CLIENT_ID", "env-client")
	t.Setenv("OIDC_CLIENT_SECRET", "env-secret")
	t.Setenv("OIDC_INITIAL_ADMIN_USERS", "envadmin")
	t.Setenv("HERMES_AUTH_MODE", "builtin")
	t.Setenv("HERMES_INITIAL_ADMIN_EMAIL", "env@admin")
	t.Setenv("HERMES_INITIAL_ADMIN_PASSWORD", "envpass")

	cfg, err := Load("/tmp/hermes_nonexistent_server_config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:7070", cfg.Server.Listen)
	assert.Equal(t, "postgres://env:5432/hermes", cfg.Postgres.DSN)
	assert.True(t, cfg.OIDC.Enabled)
	assert.Equal(t, "https://env-issuer", cfg.OIDC.Issuer)
	assert.Equal(t, "env-client", cfg.OIDC.ClientID)
	assert.Equal(t, "env-secret", cfg.OIDC.ClientSecret)
	assert.Equal(t, "envadmin", cfg.OIDC.InitialAdminUsers)
	assert.Equal(t, "builtin", cfg.AuthMode)
	assert.Equal(t, "env@admin", cfg.BuiltinAuth.InitialAdminEmail)
	assert.Equal(t, "envpass", cfg.BuiltinAuth.InitialAdminPassword)
}

func TestLoad_OIDCEnabledSetsAuthMode(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "1")

	cfg, err := Load("/tmp/hermes_nonexistent_server_config.yaml")
	require.NoError(t, err)

	assert.True(t, cfg.OIDC.Enabled)
	assert.Equal(t, "oidc", cfg.AuthMode, "auth_mode should default to oidc when OIDC is enabled")
}

func TestLoad_ExplicitAuthModeOverridesOIDC(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("HERMES_AUTH_MODE", "builtin")

	cfg, err := Load("/tmp/hermes_nonexistent_server_config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "builtin", cfg.AuthMode, "explicit auth_mode should take precedence")
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	yaml := `
server:
  listen: "0.0.0.0:9999"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(yaml), 0644))

	t.Setenv("HERMES_LISTEN", "0.0.0.0:1111")

	cfg, err := Load(tmp)
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:1111", cfg.Server.Listen)
}
