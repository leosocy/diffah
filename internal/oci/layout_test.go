package oci

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadDirManifest_ReturnsRawBytesAndMediaType(t *testing.T) {
	dir := t.TempDir()
	// Minimal dir layout: version + manifest.json.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "version"), []byte("Directory Transport Version: 1.1\n"), 0o644))
	manifestJSON := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":0,"digest":"sha256:aa"},"layers":[]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifestJSON), 0o644))

	raw, mime, err := ReadDirManifest(dir)
	require.NoError(t, err)
	require.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", mime)
	require.Equal(t, manifestJSON, string(raw))
}

func TestReadDirManifest_ErrorOnMissingFile(t *testing.T) {
	_, _, err := ReadDirManifest(t.TempDir())
	require.Error(t, err)
}

func TestReadDirManifest_ErrorOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("not valid json"), 0o644))

	_, _, err := ReadDirManifest(dir)
	require.Error(t, err)
	require.ErrorContains(t, err, "decode manifest")
}

func TestReadDirManifest_ErrorOnEmptyMediaType(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schemaVersion":2,"mediaType":""}`), 0o644))

	_, _, err := ReadDirManifest(dir)
	require.Error(t, err)
	require.ErrorContains(t, err, "empty mediaType")
}
