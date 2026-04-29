package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigShow_TextFormat(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("HOME", t.TempDir()) // ensure no real config interferes

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "config", "show")
	require.Equal(t, 0, rc)

	out := stdout.String()
	require.Contains(t, out, "platform: linux/amd64")
}

func TestConfigShow_JSONFormat(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("HOME", t.TempDir())

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "--format=json", "config", "show")
	require.Equal(t, 0, rc)

	out := stdout.String()
	// Output must use the writeJSON envelope (schema_version + data) and
	// kebab-case keys from the json struct tags, not Go PascalCase.
	require.Contains(t, out, `"data":`)
	require.Contains(t, out, `"platform": "linux/amd64"`)
}

func TestConfigInit_WritesTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "config", "init", path)
	require.Equal(t, 0, rc)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "platform: linux/amd64")
}

func TestConfigInit_HonorsDIFFAH_CONFIG(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "custom.yaml")
	t.Setenv("DIFFAH_CONFIG", target)

	rc := Run(nil, nil, "config", "init")
	require.Equal(t, 0, rc)

	body, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(body), "platform: linux/amd64")
}

func TestConfigInit_RefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# existing\n"), 0o600))

	var stderr bytes.Buffer
	rc := Run(nil, &stderr, "config", "init", path)
	require.NotEqual(t, 0, rc)
	require.Contains(t, stderr.String(), "already exists")
}

func TestConfigInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))

	rc := Run(nil, nil, "config", "init", "--force", path)
	require.Equal(t, 0, rc)

	body, _ := os.ReadFile(path)
	require.Contains(t, string(body), "platform:")
}

func TestConfigInit_OutputIsLoadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.yaml")
	require.Equal(t, 0, Run(nil, nil, "config", "init", path))
	require.Equal(t, 0, Run(nil, nil, "config", "validate", path))
}

func TestConfigValidate_OKForValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/amd64\n"), 0o644))

	rc := Run(nil, nil, "config", "validate", path)
	require.Equal(t, 0, rc)
}

func TestConfigValidate_ExitsTwoOnInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644))

	var stderr bytes.Buffer
	rc := Run(nil, &stderr, "config", "validate", path)
	require.Equal(t, 2, rc)
	require.Contains(t, stderr.String(), "config:")
}
