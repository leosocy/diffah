package importer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestLayerSetDiff(t *testing.T) {
	expected := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:b", Size: 20},
	}
	actual := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:c", Size: 30},
	}
	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing) != 1 || missing[0] != "sha256:b" {
		t.Errorf("missing = %v, want [sha256:b]", missing)
	}
	if len(unexpected) != 1 || unexpected[0] != "sha256:c" {
		t.Errorf("unexpected = %v, want [sha256:c]", unexpected)
	}
}

func TestLayerSetDiff_Empty(t *testing.T) {
	missing, unexpected := layerSetDiff(nil, nil)
	if len(missing) != 0 || len(unexpected) != 0 {
		t.Errorf("expected empty diffs, got missing=%v unexpected=%v", missing, unexpected)
	}
}

func TestVerifyPerLayerSize_Matches(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 100}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	if err := verifyPerLayerSize(expected, actual, blobs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyPerLayerSize_Mismatch(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 999}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	err := verifyPerLayerSize(expected, actual, blobs)
	if err == nil {
		t.Fatal("expected size mismatch error, got nil")
	}
}

// writeBlobToTempDir lays out a single blob at <tmpDir>/<algo>/<encoded>,
// matching the directory shape readSidecarTargetLayers expects from a
// bundle's blobDir. Returns the temp dir path.
func writeBlobToTempDir(t *testing.T, d digest.Digest, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	algoDir := filepath.Join(dir, d.Algorithm().String())
	if err := os.MkdirAll(algoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, d.Encoded()), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// parseAsDestForTest exposes parseManifestLayers via a name that mirrors
// readDestManifestLayers' signature, sidestepping the NewImageSource dance
// that requires a real dest in unit tests.
func parseAsDestForTest(raw []byte, mediaType string) ([]LayerRef, string, digest.Digest, error) {
	layers, mt, err := parseManifestLayers(raw, mediaType)
	if err != nil {
		return nil, "", "", err
	}
	return layers, mt, digest.FromBytes(raw), nil
}

func TestVerifyApplyInvariant_HappyPath(t *testing.T) {
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100}]}`)
	mfDigest := digest.FromBytes(mfBytes)

	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: &diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				mfDigest:                   {Size: int64(len(mfBytes))},
				digest.Digest("sha256:l1"): {Size: 100},
			},
			Images: []diff.ImageEntry{
				{
					Name: "svc-a",
					Target: diff.TargetRef{
						ManifestDigest: mfDigest,
						MediaType:      "application/vnd.oci.image.manifest.v1+json",
					},
				},
			},
		},
	}

	expected, _, err := readSidecarTargetLayers(bundle, bundle.sidecar.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	actual, _, _, err := parseAsDestForTest(mfBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatal(err)
	}

	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing)+len(unexpected) != 0 {
		t.Errorf("happy path expected no diff; missing=%v unexpected=%v",
			missing, unexpected)
	}
}

func TestVerifyApplyInvariant_LayerMissing(t *testing.T) {
	expected := []LayerRef{
		{Digest: "sha256:a", Size: 100},
		{Digest: "sha256:b", Size: 200},
	}
	actual := []LayerRef{
		{Digest: "sha256:a", Size: 100},
	}
	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing) != 1 || missing[0] != "sha256:b" {
		t.Errorf("Missing should be [sha256:b], got %v", missing)
	}
	if len(unexpected) != 0 {
		t.Errorf("Unexpected should be empty, got %v", unexpected)
	}
}

func TestVerifyApplyInvariant_AcrossSchemaConversion(t *testing.T) {
	// Two manifests with the same layer set but different mediaTypes
	// (OCI v1 vs Docker schema 2). Their bytes — and therefore their
	// manifest digests — differ, but the invariant must still report no
	// layer-set diff because copy.Image legitimately rewrites manifests
	// across schema conversions; layer bytes and sizes never change.
	ociBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100}]}`)
	dockerBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l1","size":100}]}`)

	expectedLayers, _, err := parseManifestLayers(ociBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatal(err)
	}
	actualLayers, _, err := parseManifestLayers(dockerBytes,
		"application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		t.Fatal(err)
	}

	missing, unexpected := layerSetDiff(expectedLayers, actualLayers)
	if len(missing)+len(unexpected) != 0 {
		t.Errorf("layer set must match across schema conversion; missing=%v unexpected=%v",
			missing, unexpected)
	}
}

// fakeInvariantDestRef + fakeInvariantSrc let TestVerifyApplyInvariant_*
// drive verifyApplyInvariant end-to-end without a real registry/archive.
// Only the methods readDestManifestLayers calls (NewImageSource, GetManifest,
// Close) are populated — others are stubbed. Modeled on fakeDestRef /
// fakeMimeSrc in compose_compat_test.go (kept separate to avoid cross-test
// coupling).
type fakeInvariantDestRef struct{ src types.ImageSource }

type fakeInvariantTransport struct{}

func (fakeInvariantTransport) Name() string                                          { return "fake-invariant" }
func (fakeInvariantTransport) ParseReference(_ string) (types.ImageReference, error) { return nil, nil }
func (fakeInvariantTransport) ValidatePolicyConfigurationScope(_ string) error       { return nil }

func (r *fakeInvariantDestRef) Transport() types.ImageTransport { return fakeInvariantTransport{} }
func (*fakeInvariantDestRef) StringWithinTransport() string     { return "fake://invariant" }
func (*fakeInvariantDestRef) DockerReference() reference.Named  { return nil }
func (*fakeInvariantDestRef) PolicyConfigurationIdentity() string {
	return ""
}
func (*fakeInvariantDestRef) PolicyConfigurationNamespaces() []string { return nil }
func (*fakeInvariantDestRef) NewImage(context.Context, *types.SystemContext) (types.ImageCloser, error) {
	return nil, nil
}
func (r *fakeInvariantDestRef) NewImageSource(context.Context, *types.SystemContext) (types.ImageSource, error) {
	return r.src, nil
}
func (*fakeInvariantDestRef) NewImageDestination(context.Context, *types.SystemContext) (types.ImageDestination, error) {
	return nil, nil
}
func (*fakeInvariantDestRef) DeleteImage(context.Context, *types.SystemContext) error { return nil }

type fakeInvariantSrc struct {
	raw  []byte
	mime string
}

func (s *fakeInvariantSrc) Reference() types.ImageReference { return nil }
func (s *fakeInvariantSrc) Close() error                    { return nil }
func (s *fakeInvariantSrc) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return s.raw, s.mime, nil
}
func (*fakeInvariantSrc) HasThreadSafeGetBlob() bool { return false }
func (*fakeInvariantSrc) GetBlob(context.Context, types.BlobInfo, types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}
func (*fakeInvariantSrc) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (*fakeInvariantSrc) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}

// TestVerifyApplyInvariant_PassesAcrossSchemaConversion drives the
// orchestrator end-to-end with a sidecar expecting an OCI manifest and a
// dest returning a Docker schema 2 manifest with the same layer set.
// This pins the conditional manifest-digest check at invariant.go's
// expectedMediaType == actualMediaType guard — without that guard, the
// test would fail because the two manifest blobs digest to different
// values.
func TestVerifyApplyInvariant_PassesAcrossSchemaConversion(t *testing.T) {
	ociBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100}]}`)
	dockerBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l1","size":100}]}`)
	ociDigest := digest.FromBytes(ociBytes)

	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, ociDigest, ociBytes),
		sidecar: &diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				ociDigest:                  {Size: int64(len(ociBytes))},
				digest.Digest("sha256:l1"): {Size: 100},
			},
			Images: []diff.ImageEntry{
				{
					Name: "svc-a",
					Target: diff.TargetRef{
						ManifestDigest: ociDigest,
						MediaType:      "application/vnd.oci.image.manifest.v1+json",
					},
				},
			},
		},
	}

	destRef := &fakeInvariantDestRef{src: &fakeInvariantSrc{
		raw:  dockerBytes,
		mime: "application/vnd.docker.distribution.manifest.v2+json",
	}}

	if err := verifyApplyInvariant(context.Background(),
		bundle.sidecar.Images[0], bundle, destRef, nil); err != nil {
		t.Errorf("schema conversion (OCI sidecar -> Docker schema 2 dest with same layer set) "+
			"must pass; got %v", err)
	}
}
