package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

// stubRef implements types.ImageReference for testing purposes.
type stubRef struct{}

func (stubRef) Transport() types.ImageTransport                    { return nil }
func (stubRef) StringWithinTransport() string                      { return "test://baseline" }
func (stubRef) DockerReference() reference.Named                   { return nil }
func (stubRef) PolicyConfigurationIdentity() string                { return "" }
func (stubRef) PolicyConfigurationNamespaces() []string            { return nil }
func (stubRef) NewImage(context.Context, *types.SystemContext) (types.ImageCloser, error) {
	return nil, nil
}
func (stubRef) NewImageSource(context.Context, *types.SystemContext) (types.ImageSource, error) {
	return nil, nil
}
func (stubRef) NewImageDestination(context.Context, *types.SystemContext) (types.ImageDestination, error) {
	return nil, nil
}
func (stubRef) DeleteImage(context.Context, *types.SystemContext) error { return nil }

var _ types.ImageReference = stubRef{}

// fakeSource implements types.ImageSource. GetBlob returns a strings.Reader
// for digests in blobs; missing digests return an error wrapping os.ErrNotExist.
type fakeSource struct {
	blobs       map[digest.Digest]string
	manifestRaw []byte
	manifestMT  string
	closeCalls  int
}

func (f *fakeSource) Reference() types.ImageReference { return stubRef{} }
func (f *fakeSource) Close() error                    { f.closeCalls++; return nil }
func (f *fakeSource) GetManifest(_ context.Context, _ *digest.Digest) ([]byte, string, error) {
	return f.manifestRaw, f.manifestMT, nil
}
func (f *fakeSource) GetBlob(_ context.Context, info types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	if data, ok := f.blobs[info.Digest]; ok {
		return io.NopCloser(strings.NewReader(data)), int64(len(data)), nil
	}
	return nil, 0, fmt.Errorf("blob %s: %w", info.Digest, os.ErrNotExist)
}
func (f *fakeSource) HasThreadSafeGetBlob() bool { return false }
func (f *fakeSource) GetSignatures(_ context.Context, _ *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (f *fakeSource) LayerInfosForCopy(_ context.Context, _ *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}

func makeSidecar(shipped []diff.BlobRef, required []diff.BlobRef) *diff.Sidecar {
	return &diff.Sidecar{
		Version:              "v1",
		Tool:                 "diffah",
		ToolVersion:          "test",
		Platform:             "linux/amd64",
		Target:               diff.ImageRef{ManifestDigest: "sha256:target", MediaType: "m"},
		Baseline:             diff.BaselineRef{ManifestDigest: "sha256:base", MediaType: "m"},
		ShippedInDelta:       shipped,
		RequiredFromBaseline: required,
	}
}

func TestComposite_GetBlob_RequiredEntry_FetchesFromBaseline(t *testing.T) {
	reqDigest := digest.Digest("sha256:baseblob")
	delta := &fakeSource{blobs: map[digest.Digest]string{reqDigest: "from-delta"}}
	baseline := &fakeSource{blobs: map[digest.Digest]string{reqDigest: "from-baseline"}}
	sidecar := makeSidecar(
		[]diff.BlobRef{},
		[]diff.BlobRef{{Digest: reqDigest, Size: 13, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}},
	)
	c := NewCompositeSource(delta, baseline, sidecar)

	r, sz, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: reqDigest}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(len("from-baseline")), sz)
	defer r.Close()
	b, _ := io.ReadAll(r)
	require.Equal(t, "from-baseline", string(b))
}

func TestComposite_GetBlob_FullEntry_FetchesFromDelta(t *testing.T) {
	fullDigest := digest.Digest("sha256:fulllayer")
	delta := &fakeSource{blobs: map[digest.Digest]string{fullDigest: "delta-full-data"}}
	baseline := &fakeSource{blobs: map[digest.Digest]string{}}
	sidecar := makeSidecar(
		[]diff.BlobRef{{
			Digest:      fullDigest,
			Size:        15,
			MediaType:   "application/vnd.oci.image.layer.v1.tar+gzip",
			Encoding:    diff.EncodingFull,
			ArchiveSize: 15,
		}},
		[]diff.BlobRef{},
	)
	c := NewCompositeSource(delta, baseline, sidecar)

	r, sz, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: fullDigest}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(len("delta-full-data")), sz)
	defer r.Close()
	b, _ := io.ReadAll(r)
	require.Equal(t, "delta-full-data", string(b))
}

func TestComposite_GetBlob_PatchEntry_ReassemblesViaBaseline(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not on PATH, skipping patch reassembly test")
	}

	// Build ref and target data.
	refBytes := bytes.Repeat([]byte{0x11}, 1<<12)
	target := make([]byte, len(refBytes))
	copy(target, refBytes)
	target[42] = 0xFF // single-byte difference

	// Encode the patch.
	patchBytes, err := zstdpatch.Encode(refBytes, target)
	require.NoError(t, err)

	// Digests.
	refDigest := digest.FromBytes(refBytes)
	targetDigest := digest.FromBytes(target)

	delta := &fakeSource{blobs: map[digest.Digest]string{targetDigest: string(patchBytes)}}
	baseline := &fakeSource{blobs: map[digest.Digest]string{refDigest: string(refBytes)}}
	sidecar := makeSidecar(
		[]diff.BlobRef{{
			Digest:          targetDigest,
			Size:            int64(len(target)),
			MediaType:       "application/vnd.oci.image.layer.v1.tar+gzip",
			Encoding:        diff.EncodingPatch,
			Codec:           "zstd",
			PatchFromDigest: refDigest,
			ArchiveSize:     int64(len(patchBytes)),
		}},
		[]diff.BlobRef{},
	)
	c := NewCompositeSource(delta, baseline, sidecar)

	r, sz, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: targetDigest}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(len(target)), sz)
	defer r.Close()
	b, _ := io.ReadAll(r)
	require.Equal(t, target, b)
}

func TestComposite_GetManifest_DelegatesToDelta(t *testing.T) {
	delta := &fakeSource{manifestRaw: []byte("delta-manifest"), manifestMT: "application/delta"}
	baseline := &fakeSource{manifestRaw: []byte("baseline-manifest"), manifestMT: "application/baseline"}
	sidecar := makeSidecar([]diff.BlobRef{}, []diff.BlobRef{})
	c := NewCompositeSource(delta, baseline, sidecar)

	raw, mt, err := c.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "delta-manifest", string(raw))
	require.Equal(t, "application/delta", mt)
}

func TestComposite_Close_ClosesBoth(t *testing.T) {
	delta := &fakeSource{}
	baseline := &fakeSource{}
	sidecar := makeSidecar([]diff.BlobRef{}, []diff.BlobRef{})
	c := NewCompositeSource(delta, baseline, sidecar)
	require.NoError(t, c.Close())
	require.Equal(t, 1, delta.closeCalls)
	require.Equal(t, 1, baseline.closeCalls)
}

func TestProbeBaseline_MissingPatchRef_RaisesErr(t *testing.T) {
	manifestJSON := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"mediaType": "application/vnd.oci.image.config.v1+json",
				   "size": 1, "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"layers": [{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
					"size": 10, "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]
	}`
	src := &fakeSource{
		manifestRaw: []byte(manifestJSON),
		manifestMT:  "application/vnd.oci.image.manifest.v1+json",
	}

	sc := &diff.Sidecar{
		Version: "v1", Tool: "diffah", ToolVersion: "t", Platform: "linux/amd64",
		Target:   diff.ImageRef{ManifestDigest: "sha256:tgt", MediaType: "m"},
		Baseline: diff.BaselineRef{ManifestDigest: "sha256:b", MediaType: "m"},
		RequiredFromBaseline: []diff.BlobRef{},
		ShippedInDelta: []diff.BlobRef{{
			Digest: "sha256:tgt", Size: 100, MediaType: "m",
			Encoding: diff.EncodingPatch, Codec: "zstd-patch",
			PatchFromDigest: "sha256:ref", ArchiveSize: 10,
		}},
	}

	err := probeBaseline(context.Background(), src, sc)
	var miss *diff.ErrBaselineMissingPatchRef
	require.ErrorAs(t, err, &miss)
	require.Equal(t, "sha256:ref", miss.Digest)
}
