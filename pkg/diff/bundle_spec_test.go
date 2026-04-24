package diff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBundleSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tar"), []byte{}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.tar"), []byte{}, 0o600))
	raw := []byte(`{
		"pairs": [
			{"name":"service-a","baseline":"docker-archive:a.tar","target":"docker-archive:b.tar"}
		]
	}`)
	specPath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBundleSpec(specPath)
	require.NoError(t, err)
	require.Len(t, spec.Pairs, 1)
	require.Equal(t, "service-a", spec.Pairs[0].Name)
	require.Equal(t, "docker-archive:"+filepath.Join(dir, "a.tar"), spec.Pairs[0].Baseline)
	require.Equal(t, "docker-archive:"+filepath.Join(dir, "b.tar"), spec.Pairs[0].Target)
}

func TestParseBaselineSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"baselines":{"service-a":"oci-archive:/tmp/a.tar"}}`)
	specPath := filepath.Join(dir, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBaselineSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"service-a": "oci-archive:/tmp/a.tar",
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
		{"missing name", `{"pairs":[{"baseline":"docker-archive:a","target":"docker-archive:b"}]}`, "name"},
		{"duplicate name", `{"pairs":[{"name":"x","baseline":"docker-archive:a","target":"docker-archive:b"},{"name":"x","baseline":"docker-archive:c","target":"docker-archive:d"}]}`, "duplicate"},
		{"bad name", `{"pairs":[{"name":"-bad","baseline":"docker-archive:a","target":"docker-archive:b"}]}`, "name"},
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

func TestParseBundleSpec_BarePathRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.json")
	if err := os.WriteFile(path, []byte(`{
  "pairs": [
    {"name": "svc-a", "baseline": "v1/svc-a.tar", "target": "v2/svc-a.tar"}
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseBundleSpec(path)
	if err == nil {
		t.Fatal("expected error on bare-path baseline")
	}
	var missing *ErrBundleSpecMissingTransport
	if !errors.As(err, &missing) {
		t.Fatalf("want ErrBundleSpecMissingTransport, got %T: %v", err, err)
	}
	if missing.FieldPath != "pairs[0].baseline" {
		t.Errorf("FieldPath = %q, want pairs[0].baseline", missing.FieldPath)
	}
	if !strings.Contains(err.Error(), "docker-archive:") {
		t.Error("migration hint should mention 'docker-archive:' prefix")
	}
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
