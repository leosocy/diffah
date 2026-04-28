package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := Load(missing)

	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, Default(), cfg)
}
