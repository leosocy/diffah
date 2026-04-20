package importer

/*
ImageSource interface (go.podman.io/image/v5/types):

    Reference() ImageReference
    Close() error
    GetManifest(ctx context.Context, instanceDigest *digest.Digest) ([]byte, string, error)
    GetBlob(ctx context.Context, info BlobInfo, cache BlobInfoCache) (io.ReadCloser, int64, error)
    HasThreadSafeGetBlob() bool
    GetSignatures(ctx context.Context, instanceDigest *digest.Digest) ([][]byte, error)
    LayerInfosForCopy(ctx context.Context, instanceDigest *digest.Digest) ([]BlobInfo, error)

digest.Digest is from github.com/opencontainers/go-digest.
*/

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

// CompositeSource implements types.ImageSource by classifying each blob
// digest against a sidecar and dispatching:
//
//   - RequiredFromBaseline entries are fetched from the baseline source.
//   - ShippedInDelta entries with encoding=full are fetched from the delta.
//   - ShippedInDelta entries with encoding=patch are fetched from the delta,
//     decoded against a baseline reference blob, and verified digest-wise.
type CompositeSource struct {
	delta    types.ImageSource
	baseline types.ImageSource
	shipped  map[digest.Digest]diff.BlobRef
	required map[digest.Digest]diff.BlobRef
}

func NewCompositeSource(
	delta, baseline types.ImageSource, sidecar *diff.Sidecar,
) *CompositeSource {
	shipped := make(map[digest.Digest]diff.BlobRef, len(sidecar.ShippedInDelta))
	for _, e := range sidecar.ShippedInDelta {
		shipped[e.Digest] = e
	}
	required := make(map[digest.Digest]diff.BlobRef, len(sidecar.RequiredFromBaseline))
	for _, e := range sidecar.RequiredFromBaseline {
		required[e.Digest] = e
	}
	return &CompositeSource{
		delta: delta, baseline: baseline,
		shipped: shipped, required: required,
	}
}

func (c *CompositeSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	if _, ok := c.required[info.Digest]; ok {
		return c.baseline.GetBlob(ctx, info, cache)
	}
	entry, ok := c.shipped[info.Digest]
	if !ok {
		// Unknown digest (config blob, etc.) — delegate to delta.
		return c.delta.GetBlob(ctx, info, cache)
	}
	switch entry.Encoding {
	case diff.EncodingFull:
		return c.delta.GetBlob(ctx, info, cache)
	case diff.EncodingPatch:
		return c.fetchPatched(ctx, entry, cache)
	default:
		return nil, 0, fmt.Errorf("composite: unknown encoding %q for %s",
			entry.Encoding, info.Digest)
	}
}

func (c *CompositeSource) fetchPatched(
	ctx context.Context, entry diff.BlobRef, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	ref, err := readAllBlob(c.baseline, ctx, types.BlobInfo{Digest: entry.PatchFromDigest}, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("composite: fetch patch reference %s: %w",
			entry.PatchFromDigest, err)
	}
	patch, err := readAllBlob(c.delta, ctx, types.BlobInfo{Digest: entry.Digest}, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("composite: fetch patch bytes %s: %w",
			entry.Digest, err)
	}
	assembled, err := zstdpatch.Decode(ref, patch)
	if err != nil {
		return nil, 0, fmt.Errorf("composite: decode patch %s: %w", entry.Digest, err)
	}
	if got := digest.FromBytes(assembled); got != entry.Digest {
		return nil, 0, &diff.ErrIntraLayerAssemblyMismatch{
			Digest: entry.Digest.String(), Got: got.String(),
		}
	}
	return io.NopCloser(bytes.NewReader(assembled)), int64(len(assembled)), nil
}

func readAllBlob(
	src types.ImageSource, ctx context.Context,
	info types.BlobInfo, cache types.BlobInfoCache,
) ([]byte, error) {
	r, _, err := src.GetBlob(ctx, info, cache)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// GetManifest delegates to the delta (the authoritative holder of the target
// manifest in the import pipeline).
func (c *CompositeSource) GetManifest(ctx context.Context, instanceDigest *digest.Digest) ([]byte, string, error) {
	return c.delta.GetManifest(ctx, instanceDigest)
}

// Reference, Close, HasThreadSafeGetBlob, GetSignatures, LayerInfosForCopy
// all delegate to the delta — except Close, which closes both.

func (c *CompositeSource) Reference() types.ImageReference { return c.delta.Reference() }

func (c *CompositeSource) Close() error {
	errDelta := c.delta.Close()
	errBaseline := c.baseline.Close()
	if errDelta != nil {
		return errDelta
	}
	return errBaseline
}

func (c *CompositeSource) HasThreadSafeGetBlob() bool {
	return c.delta.HasThreadSafeGetBlob() && c.baseline.HasThreadSafeGetBlob()
}

func (c *CompositeSource) GetSignatures(ctx context.Context, instanceDigest *digest.Digest) ([][]byte, error) {
	return c.delta.GetSignatures(ctx, instanceDigest)
}

func (c *CompositeSource) LayerInfosForCopy(
	ctx context.Context, instanceDigest *digest.Digest,
) ([]types.BlobInfo, error) {
	return c.delta.LayerInfosForCopy(ctx, instanceDigest)
}

// Compile-time interface check.
var _ types.ImageSource = (*CompositeSource)(nil)
