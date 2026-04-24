package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "bundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah bundle BUNDLE-SPEC DELTA-OUT")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BUNDLE-SPEC")
	require.Contains(t, out, "DELTA-OUT")
}

func TestBundleCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "only-one.json")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'bundle' requires 2 arguments")
}

func TestBundleCommand_RejectsMissingSpecFile(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "/tmp/does-not-exist.json", "bundle.tar")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "bundle spec")
}

func TestBundleCommand_AcceptsSpecFile(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "spec.json")
	spec := map[string]any{
		"pairs": []map[string]string{
			{"name": "app", "baseline": "b.tar", "target": "t.tar"},
		},
	}
	raw, _ := json.Marshal(spec)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "--dry-run", specPath, filepath.Join(tmp, "bundle.tar"))
	require.NotEqual(t, 2, code, "exit 2 indicates CLI rejected args; stderr: %s", stderr.String())
}
