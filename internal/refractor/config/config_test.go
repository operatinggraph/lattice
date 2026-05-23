package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

const validConfig = `
nats:
  url: nats://localhost:4222
core_kv_bucket: core
adj_kv_bucket: adjacency
`

func TestLoad_Basic(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "nats://localhost:4222", cfg.NATS.URL)
	assert.Empty(t, cfg.NATS.CredentialsFile)
	assert.Equal(t, "core", cfg.CoreKVBucket)
	assert.Equal(t, "adjacency", cfg.AdjKVBucket)
}

func TestLoad_CredentialsFile(t *testing.T) {
	path := writeConfig(t, `
nats:
  url: nats://localhost:4222
  credentials_file: /path/to/creds
core_kv_bucket: core
adj_kv_bucket: adjacency
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/path/to/creds", cfg.NATS.CredentialsFile)
}

func TestLoad_EnvOverride_URL(t *testing.T) {
	path := writeConfig(t, `
nats:
  url: nats://file-host:4222
core_kv_bucket: core
adj_kv_bucket: adjacency
`)
	t.Setenv("NATS_URL", "nats://env-host:4222")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "nats://env-host:4222", cfg.NATS.URL)
}

func TestLoad_EnvOverride_CredentialsFile(t *testing.T) {
	path := writeConfig(t, `
nats:
  url: nats://localhost:4222
  credentials_file: /file/path.creds
core_kv_bucket: core
adj_kv_bucket: adjacency
`)
	t.Setenv("NATS_CREDENTIALS_FILE", "/env/path.creds")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/env/path.creds", cfg.NATS.CredentialsFile)
}

func TestLoad_MissingURL(t *testing.T) {
	path := writeConfig(t, "nats:\n  credentials_file: /path/to/creds\ncore_kv_bucket: core\nadj_kv_bucket: adjacency\n")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats.url is required")
}

func TestLoad_MissingCoreKVBucket(t *testing.T) {
	path := writeConfig(t, "nats:\n  url: nats://localhost:4222\nadj_kv_bucket: adjacency\n")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "core_kv_bucket is required")
}

func TestLoad_MissingAdjKVBucket(t *testing.T) {
	path := writeConfig(t, "nats:\n  url: nats://localhost:4222\ncore_kv_bucket: core\n")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adj_kv_bucket is required")
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open config file")
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeConfig(t, "not: valid: yaml: [\n")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode config")
}

func TestLoad_EnvURLOverridesEmptyFile(t *testing.T) {
	// File has no URL, but env provides it — should succeed
	path := writeConfig(t, "nats: {}\ncore_kv_bucket: core\nadj_kv_bucket: adjacency\n")
	t.Setenv("NATS_URL", "nats://env-host:4222")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "nats://env-host:4222", cfg.NATS.URL)
	assert.Empty(t, cfg.NATS.CredentialsFile)
}
