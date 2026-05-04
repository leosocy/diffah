package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		spool:        NewBaselineSpool(t.TempDir()),
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
		spool:        NewBaselineSpool(t.TempDir()),
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
			BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
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
		spool:        NewBaselineSpool(t.TempDir()),
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
			BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
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
		spool:        NewBaselineSpool(t.TempDir()),
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
			BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
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
		spool:        NewBaselineSpool(t.TempDir()),
	}
	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: requiredDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(got)), size)
	require.Equal(t, requiredDigest, digest.FromBytes(got))
}

// TestServePatch_BlobNotFound_WrapsB1 verifies that when servePatch's
// baseline fetch surfaces a "blob not found" signal (registry-shape
// "blob unknown to registry" string here), the error is wrapped as
// *ErrMissingPatchSource carrying the originating image, the shipped
// patch digest, and the missing patch-from digest. Other failure modes
// (auth/TLS/network/timeout) keep their existing fmt.Errorf wrapping —
// covered indirectly by the existing CategoryEnvironment classification
// in pkg/diff/classify_registry.go.
func TestServePatch_BlobNotFound_WrapsB1(t *testing.T) {
	patchBytes := []byte("ignored")
	target := digest.FromBytes([]byte("target"))
	patchSrc := digest.FromBytes([]byte("missing-source"))

	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(filepath.Join(blobDir, target.Algorithm().String()), 0o755); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(blobDir, target.Algorithm().String(), target.Encoded())
	if err := os.WriteFile(patchPath, patchBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	src := &bundleImageSource{
		blobDir:   blobDir,
		imageName: "svc-x",
		baseline:  &fakeBlobNotFoundSource{},
		spool:     NewBaselineSpool(t.TempDir()),
		sidecar:   &diff.Sidecar{},
	}
	entry := diff.BlobEntry{
		Encoding:        diff.EncodingPatch,
		PatchFromDigest: patchSrc,
	}
	_, _, err := src.servePatch(context.Background(), target, entry, nil)

	var b1 *ErrMissingPatchSource
	if !errors.As(err, &b1) {
		t.Fatalf("expected ErrMissingPatchSource, got %T: %v", err, err)
	}
	if b1.PatchFromDigest != patchSrc {
		t.Errorf("PatchFromDigest = %v, want %v", b1.PatchFromDigest, patchSrc)
	}
	if b1.ShippedDigest != target {
		t.Errorf("ShippedDigest = %v, want %v", b1.ShippedDigest, target)
	}
	if b1.ImageName != "svc-x" {
		t.Errorf("ImageName = %q, want %q", b1.ImageName, "svc-x")
	}
}

// fakeBlobNotFoundSource is a minimal types.ImageSource that always returns
// a "blob unknown" error from GetBlob — the registry-shape error verified
// in pkg/importer/errors_test.go::TestIsBlobNotFound.
type fakeBlobNotFoundSource struct{}

func (*fakeBlobNotFoundSource) Reference() types.ImageReference { return nil }
func (*fakeBlobNotFoundSource) Close() error                    { return nil }
func (*fakeBlobNotFoundSource) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}
func (*fakeBlobNotFoundSource) HasThreadSafeGetBlob() bool { return true }
func (*fakeBlobNotFoundSource) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (*fakeBlobNotFoundSource) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (*fakeBlobNotFoundSource) GetBlob(context.Context, types.BlobInfo, types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("fetching blob: blob unknown to registry")
}

// TestGetBlob_BaselineOnlyMissing_WrapsB2 verifies that when GetBlob's
// baseline-only-reuse branch (the path taken when the target manifest
// references a layer the bundle did not ship) hits a "blob not found"
// signal from the baseline, the error is wrapped as
// *ErrMissingBaselineReuseLayer carrying the image name and the missing
// layer digest. Auth/TLS/network errors keep their existing fmt.Errorf
// wrapping (covered indirectly by classify_registry.go's
// CategoryEnvironment classification).
func TestGetBlob_BaselineOnlyMissing_WrapsB2(t *testing.T) {
	missing := digest.FromBytes([]byte("missing-baseline-layer"))

	src := &bundleImageSource{
		blobDir:   t.TempDir(),
		imageName: "svc-y",
		baseline:  &fakeBlobNotFoundSource{},
		spool:     NewBaselineSpool(t.TempDir()),
		sidecar:   &diff.Sidecar{Blobs: map[digest.Digest]diff.BlobEntry{}},
	}
	_, _, err := src.GetBlob(context.Background(),
		types.BlobInfo{Digest: missing}, nil)

	var b2 *ErrMissingBaselineReuseLayer
	if !errors.As(err, &b2) {
		t.Fatalf("expected ErrMissingBaselineReuseLayer, got %T: %v", err, err)
	}
	if b2.LayerDigest != missing {
		t.Errorf("LayerDigest = %v, want %v", b2.LayerDigest, missing)
	}
	if b2.ImageName != "svc-y" {
		t.Errorf("ImageName = %v, want svc-y", b2.ImageName)
	}
}
