package diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBundleSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tar"), []byte{}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.tar"), []byte{}, 0o600))
	raw := []byte(`{
		"pairs": [
			{"name":"service-a","baseline":"a.tar","target":"b.tar"}
		]
	}`)
	specPath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBundleSpec(specPath)
	require.NoError(t, err)
	require.Len(t, spec.Pairs, 1)
	require.Equal(t, "service-a", spec.Pairs[0].Name)
	require.Equal(t, filepath.Join(dir, "a.tar"), spec.Pairs[0].Baseline)
	require.Equal(t, filepath.Join(dir, "b.tar"), spec.Pairs[0].Target)
}

func TestParseBaselineSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tar"), []byte{}, 0o600))
	raw := []byte(`{"baselines":{"service-a":"a.tar"}}`)
	specPath := filepath.Join(dir, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBaselineSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"service-a": filepath.Join(dir, "a.tar"),
	}, spec.Baselines)
}

func TestParseBaselineSpec_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad JSON", "{", "bundle spec"},
		{"empty map", `{"baselines":{}}`, "non-empty"},
		{"bad name", `{"baselines":{"-leading":"a"}}`, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			specPath := filepath.Join(dir, "spec.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tc.body), 0o600))
			_, err := ParseBaselineSpec(specPath)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestParseBundleSpec_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing name", `{"pairs":[{"baseline":"a","target":"b"}]}`, "name"},
		{"duplicate name", `{"pairs":[{"name":"x","baseline":"a","target":"b"},{"name":"x","baseline":"c","target":"d"}]}`, "duplicate"},
		{"bad name", `{"pairs":[{"name":"-bad","baseline":"a","target":"b"}]}`, "name"},
		{"bad JSON", `{invalid`, "bundle spec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			specPath := filepath.Join(dir, "bundle.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tc.body), 0o600))
			_, err := ParseBundleSpec(specPath)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}
