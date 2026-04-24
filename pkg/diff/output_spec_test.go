package diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOutputSpec_AcceptsValidTransportRefs(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	body := `{"outputs":{
		"svc-a": "docker://ghcr.io/org/svc-a:v2",
		"svc-b": "oci-archive:/tmp/svc-b.tar",
		"svc-c": "dir:/tmp/svc-c"
	}}`
	require.NoError(t, os.WriteFile(specPath, []byte(body), 0o600))

	spec, err := ParseOutputSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"svc-a": "docker://ghcr.io/org/svc-a:v2",
		"svc-b": "oci-archive:/tmp/svc-b.tar",
		"svc-c": "dir:/tmp/svc-c",
	}, spec.Outputs)
}

func TestParseOutputSpec_RejectsMissingKey(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(`{"nope": {}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outputs")
}

func TestParseOutputSpec_RejectsEmpty(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(`{"outputs":{}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outputs must be non-empty")
}

func TestParseOutputSpec_RejectsBarePath(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"outputs":{"svc-a":"/tmp/a.tar"}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
	require.Contains(t, err.Error(), "svc-a")
}

func TestParseOutputSpec_RejectsInvalidName(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "outputs.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"outputs":{"bad name!":"docker://x/y:z"}}`), 0o600))

	_, err := ParseOutputSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad name!")
}

func TestParseBaselineSpec_RejectsBarePath(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"baselines":{"svc-a":"/tmp/a.tar"}}`), 0o600))

	_, err := ParseBaselineSpec(specPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
	require.Contains(t, err.Error(), "svc-a")
}

func TestParseBaselineSpec_AcceptsTransportRefs(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, []byte(
		`{"baselines":{"svc-a":"docker://x/y:v1"}}`), 0o600))

	spec, err := ParseBaselineSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, "docker://x/y:v1", spec.Baselines["svc-a"])
}
