package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_OKForMissingFile(t *testing.T) {
	require.NoError(t, Validate(filepath.Join(t.TempDir(), "absent.yaml")))
}

func TestValidate_OKForValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/amd64\n"), 0o644))
	require.NoError(t, Validate(path))
}

func TestValidate_FailsForMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644))
	var ce *ConfigError
	require.ErrorAs(t, Validate(path), &ce)
}
