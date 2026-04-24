package importer

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
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

func TestBundleImageSource_GetBlob_PatchEncoding_DecodesAndVerifies(t *testing.T) {
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:        "svc-a",
			BaselineRef: "../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bp,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	var patchDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingPatch {
			patchDigest = d
			break
		}
	}
	if patchDigest == "" {
		t.Skip("fixtures produced no patch-encoded layer; nothing to cover")
	}

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

	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: patchDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	decoded, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(decoded)), size)
	require.Equal(t, patchDigest, digest.FromBytes(decoded), "decoded blob must match expected digest")
}

func TestBundleImageSource_GetBlob_PatchEncoding_CorruptedBlob_RaisesAssemblyMismatch(t *testing.T) {
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:        "svc-a",
			BaselineRef: "../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bp,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	var patchDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingPatch {
			patchDigest = d
			break
		}
	}
	if patchDigest == "" {
		t.Skip("fixtures produced no patch-encoded layer; nothing to cover")
	}

	patchPath := filepath.Join(b.blobDir, patchDigest.Algorithm().String(), patchDigest.Encoded())
	require.NoError(t, os.WriteFile(patchPath, []byte("not a real zstd patch"), 0o600))

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
	_, _, err = src.GetBlob(context.Background(), types.BlobInfo{Digest: patchDigest}, nil)
	require.Error(t, err)
}

func TestBundleImageSource_GetBlob_BaselineDelegation_Verified(t *testing.T) {
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:        "svc-a",
			BaselineRef: "../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "off",
		OutputPath:  bp,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	var mf struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}
	require.NoError(t, json.Unmarshal(mfBytes, &mf))

	var requiredDigest digest.Digest
	for _, l := range mf.Layers {
		if _, ok := b.sidecar.Blobs[l.Digest]; !ok {
			requiredDigest = l.Digest
			break
		}
	}
	if requiredDigest == "" {
		t.Skip("all target layers shipped; no baseline-delegated blob to cover")
	}

	src := &bundleImageSource{
		blobDir:      b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}
	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: requiredDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(got)), size)
	require.Equal(t, requiredDigest, digest.FromBytes(got))
}
