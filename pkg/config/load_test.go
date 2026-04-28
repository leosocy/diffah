package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := Load(missing)

	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, Default(), cfg)
}

func TestLoad_UnknownFieldReturnsConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
platform: linux/amd64
nonexistent-field: value
`), 0o644))

	cfg, err := Load(path)

	require.Nil(t, cfg)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	require.Contains(t, err.Error(), "nonexistent-field")
}

func TestLoad_MalformedYAMLReturnsConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: [bad nested"), 0o644))

	cfg, err := Load(path)

	require.Nil(t, cfg)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, path, ce.Path)
}

func TestLoad_ValidYAMLOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
platform: linux/arm64
intra-layer: required
zstd-level: 12
workers: 4
retry-times: 5
retry-delay: 250ms
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "linux/arm64", cfg.Platform)
	require.Equal(t, "required", cfg.IntraLayer)
	require.Equal(t, 12, cfg.ZstdLevel)
	require.Equal(t, 4, cfg.Workers)
	require.Equal(t, 5, cfg.RetryTimes)
	require.Equal(t, 250*time.Millisecond, cfg.RetryDelay)
	// Untouched fields keep defaults:
	require.Equal(t, 3, cfg.Candidates)
	require.Equal(t, "auto", cfg.ZstdWindowLog)
}
