package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnbundleCommand_HelpShowsArguments(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-SPEC")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "DELTA-IN")
	require.Contains(t, out, "BASELINE-SPEC")
	require.Contains(t, out, "OUTPUT-SPEC")
}

func TestUnbundleCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", "d.tar", "b.json")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'unbundle' requires 3 arguments")
}

func TestUnbundleCommand_AcceptsStrict(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "--strict")
}

func TestUnbundleCommand_ArgsNowRequireOutputSpec(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "DELTA-IN BASELINE-SPEC OUTPUT-SPEC")
}

func TestUnbundleCommand_RejectsMissingOutputSpec(t *testing.T) {
	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, os.WriteFile(deltaPath, []byte{}, 0o600))
	baselinesPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(baselinesPath,
		[]byte(`{"baselines":{"app":"oci-archive:/tmp/a.tar"}}`), 0o600))
	missingOutputSpec := filepath.Join(tmp, "nonexistent-outputs.json")

	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", deltaPath, baselinesPath, missingOutputSpec)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "outputs")
}

func TestUnbundleCommand_RegistersRegistryFlags(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "--authfile")
	require.Contains(t, out, "--creds")
	require.Contains(t, out, "--tls-verify")
	require.Contains(t, out, "--retry-times")
}

func TestUnbundleCommand_OutputSpecRejectsDirectory(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "some-dir")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, os.WriteFile(deltaPath, []byte{}, 0o600))
	baselinesPath := filepath.Join(tmp, "baselines.json")
	require.NoError(t, os.WriteFile(baselinesPath,
		[]byte(`{"baselines":{"x":"docker-archive:/tmp/a.tar"}}`), 0o600))

	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", deltaPath, baselinesPath, dir)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "must be a JSON file")
}
