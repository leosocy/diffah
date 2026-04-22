package importer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

func openBaseline(t *testing.T, path string) types.ImageSource {
	t.Helper()
	ref, err := imageio.OpenArchiveRef(path)
	require.NoError(t, err)
	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestBundleImageSource_GetManifest_ReturnsStoredBytes(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	src := &bundleImageSource{
		blobDir:      b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}

	gotBytes, gotMime, err := src.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, mfBytes, gotBytes)
	require.Equal(t, img.Target.MediaType, gotMime)
	require.Equal(t, digest.FromBytes(gotBytes), img.Target.ManifestDigest)
}

func TestBundleImageSource_GetBlob_FullEncoding_ReturnsVerifiedBytes(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	var fullDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingFull {
			fullDigest = d
			break
		}
	}
	require.NotEmpty(t, fullDigest)

	src := &bundleImageSource{
		blobDir:      b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}

	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: fullDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(got)), size)
	require.Equal(t, fullDigest, digest.FromBytes(got))
}
