package exporter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestManifestBaseline_ParsesLayerDigests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	manifestBody := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","size":1,"digest":"sha256:cfg"},
		"layers": [
			{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":100,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":200,"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		]
	}`
	require.NoError(t, os.WriteFile(path, []byte(manifestBody), 0o644))

	b, err := NewManifestBaseline(path, "")
	require.NoError(t, err)

	digests, err := b.LayerDigests(context.Background())
	require.NoError(t, err)
	require.Len(t, digests, 2)
	require.Equal(t, digest.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), digests[0])
	require.Equal(t, digest.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), digests[1])

	ref := b.ManifestRef()
	require.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", ref.MediaType)
	require.NotEmpty(t, ref.ManifestDigest)
	require.Equal(t, path, ref.SourceHint)
}

func TestManifestBaseline_RejectsManifestListWithoutPlatform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list.json")
	body := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[]}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, err := NewManifestBaseline(path, "")
	var lerr *diff.ErrManifestListUnselected
	require.ErrorAs(t, err, &lerr)
}

func TestManifestBaseline_ErrorOnMissingFile(t *testing.T) {
	_, err := NewManifestBaseline(filepath.Join(t.TempDir(), "nope.json"), "")
	require.Error(t, err)
}

func TestManifestBaseline_ErrorOnInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))
	_, err := NewManifestBaseline(path, "")
	require.Error(t, err)
}

func TestManifestBaseline_ErrorOnEmptyMediaType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-mime.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":2,"mediaType":""}`), 0o644))
	_, err := NewManifestBaseline(path, "")
	require.Error(t, err)
}
