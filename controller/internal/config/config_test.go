package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load("/tmp/hermes_nonexistent_config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:9080", cfg.ControlPlane.URL)
	assert.Equal(t, 5, cfg.ControlPlane.PollInterval)
	assert.Equal(t, 60, cfg.ControlPlane.ReconcileInterval)
	assert.Equal(t, "default", cfg.ControlPlane.Namespace)
	assert.Equal(t, []string{"http://127.0.0.1:2379"}, cfg.Etcd.Endpoints)
	assert.Equal(t, "/hermes/domains", cfg.Etcd.DomainPrefix)
	assert.Equal(t, "/hermes/clusters", cfg.Etcd.ClusterPrefix)
	assert.Equal(t, "/hermes/instances", cfg.Etcd.InstancePrefix)
	assert.Equal(t, "/hermes/meta", cfg.Etcd.MetaPrefix)
	assert.Empty(t, cfg.Auth.AccessKey)
	assert.Empty(t, cfg.Auth.SecretKey)
}

func TestLoad_YAMLFile(t *testing.T) {
	yaml := `
controlplane:
  url: "http://cp:9080"
  poll_interval: 10
  reconcile_interval: 120
  namespace: "staging"
etcd:
  endpoints:
    - "http://etcd1:2379"
    - "http://etcd2:2379"
  domain_prefix: "/custom/domains"
  cluster_prefix: "/custom/clusters"
  instance_prefix: "/custom/instances"
  meta_prefix: "/custom/meta"
  username: "root"
  password: "secret"
auth:
  access_key: "ak123"
  secret_key: "sk456"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(yaml), 0644))

	cfg, err := Load(tmp)
	require.NoError(t, err)

	assert.Equal(t, "http://cp:9080", cfg.ControlPlane.URL)
	assert.Equal(t, 10, cfg.ControlPlane.PollInterval)
	assert.Equal(t, 120, cfg.ControlPlane.ReconcileInterval)
	assert.Equal(t, "staging", cfg.ControlPlane.Namespace)
	assert.Equal(t, []string{"http://etcd1:2379", "http://etcd2:2379"}, cfg.Etcd.Endpoints)
	assert.Equal(t, "/custom/domains", cfg.Etcd.DomainPrefix)
	assert.Equal(t, "root", cfg.Etcd.Username)
	assert.Equal(t, "secret", cfg.Etcd.Password)
	assert.Equal(t, "ak123", cfg.Auth.AccessKey)
	assert.Equal(t, "sk456", cfg.Auth.SecretKey)
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(":::not yaml"), 0644))

	_, err := Load(tmp)
	assert.Error(t, err)
}

func TestLoad_EnvOverrides(t *testing.T) {
	envVars := map[string]string{
		"HERMES_CONTROLPLANE_URL":                "http://override:9080",
		"HERMES_CONTROLPLANE_POLL_INTERVAL":      "30",
		"HERMES_CONTROLPLANE_RECONCILE_INTERVAL": "300",
		"HERMES_CONTROLPLANE_NAMESPACE":          "production",
		"HERMES_ETCD_ENDPOINTS":                  "http://e1:2379,http://e2:2379",
		"HERMES_ETCD_DOMAIN_PREFIX":              "/env/domains",
		"HERMES_ETCD_CLUSTER_PREFIX":             "/env/clusters",
		"HERMES_ETCD_INSTANCE_PREFIX":            "/env/instances",
		"HERMES_ETCD_META_PREFIX":                "/env/meta",
		"HERMES_ETCD_USERNAME":                   "envuser",
		"HERMES_ETCD_PASSWORD":                   "envpass",
		"HERMES_AUTH_ACCESS_KEY":                 "env_ak",
		"HERMES_AUTH_SECRET_KEY":                 "env_sk",
	}

	for k, v := range envVars {
		t.Setenv(k, v)
	}

	cfg, err := Load("/tmp/hermes_nonexistent_config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "http://override:9080", cfg.ControlPlane.URL)
	assert.Equal(t, 30, cfg.ControlPlane.PollInterval)
	assert.Equal(t, 300, cfg.ControlPlane.ReconcileInterval)
	assert.Equal(t, "production", cfg.ControlPlane.Namespace)
	assert.Equal(t, []string{"http://e1:2379", "http://e2:2379"}, cfg.Etcd.Endpoints)
	assert.Equal(t, "/env/domains", cfg.Etcd.DomainPrefix)
	assert.Equal(t, "/env/clusters", cfg.Etcd.ClusterPrefix)
	assert.Equal(t, "/env/instances", cfg.Etcd.InstancePrefix)
	assert.Equal(t, "/env/meta", cfg.Etcd.MetaPrefix)
	assert.Equal(t, "envuser", cfg.Etcd.Username)
	assert.Equal(t, "envpass", cfg.Etcd.Password)
	assert.Equal(t, "env_ak", cfg.Auth.AccessKey)
	assert.Equal(t, "env_sk", cfg.Auth.SecretKey)
}

func TestLoad_EnvOverrideInvalidPollInterval(t *testing.T) {
	t.Setenv("HERMES_CONTROLPLANE_POLL_INTERVAL", "not_a_number")
	cfg, err := Load("/tmp/hermes_nonexistent_config.yaml")
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.ControlPlane.PollInterval)
}

func TestLoad_EmptyNamespaceDefaultsToDefault(t *testing.T) {
	yaml := `
controlplane:
  namespace: ""
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(yaml), 0644))

	cfg, err := Load(tmp)
	require.NoError(t, err)
	assert.Equal(t, "default", cfg.ControlPlane.Namespace)
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	yaml := `
controlplane:
  url: "http://from-yaml:9080"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(yaml), 0644))

	t.Setenv("HERMES_CONTROLPLANE_URL", "http://from-env:9080")

	cfg, err := Load(tmp)
	require.NoError(t, err)
	assert.Equal(t, "http://from-env:9080", cfg.ControlPlane.URL)
}
