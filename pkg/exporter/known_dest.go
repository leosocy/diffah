package exporter

/*
ImageDestination interface (go.podman.io/image/v5/types):

    Reference() ImageReference
    Close() error
    SupportedManifestMIMETypes() []string
    SupportsSignatures(ctx context.Context) error
    DesiredLayerCompression() LayerCompression
    AcceptsForeignLayerURLs() bool
    MustMatchRuntimeOS() bool
    IgnoresEmbeddedDockerReference() bool
    PutBlob(ctx context.Context, stream io.Reader, inputInfo BlobInfo,
        cache BlobInfoCache, isConfig bool) (BlobInfo, error)
    HasThreadSafePutBlob() bool
    TryReusingBlob(ctx context.Context, info BlobInfo, cache BlobInfoCache, canSubstitute bool) (bool, BlobInfo, error)
    PutManifest(ctx context.Context, manifest []byte, instanceDigest *digest.Digest) error
    PutSignatures(ctx context.Context, signatures [][]byte, instanceDigest *digest.Digest) error
    Commit(ctx context.Context, unparsedToplevel UnparsedImage) error
*/

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"
)

// KnownBlobsDest wraps a types.ImageDestination so that TryReusingBlob returns
// (true, info, nil) for any digest in a pre-seeded set. This lets copy.Image
// skip uploading baseline layers and produces a "partial" dir layout that
// contains only the new blobs plus the target manifest and config.
type KnownBlobsDest struct {
	inner types.ImageDestination
	known map[digest.Digest]struct{}
}

// NewKnownBlobsDest wraps dest and pre-seeds the known digest set.
func NewKnownBlobsDest(dest types.ImageDestination, known []digest.Digest) *KnownBlobsDest {
	set := make(map[digest.Digest]struct{}, len(known))
	for _, d := range known {
		set[d] = struct{}{}
	}
	return &KnownBlobsDest{inner: dest, known: set}
}

// TryReusingBlob returns (true, info, nil) immediately for known digests.
// Unknown digests are delegated so the inner destination's own reuse logic
// still fires (e.g., a registry that already has the blob on another tag).
func (d *KnownBlobsDest) TryReusingBlob(
	ctx context.Context,
	info types.BlobInfo,
	cache types.BlobInfoCache,
	canSubstitute bool,
) (bool, types.BlobInfo, error) {
	if _, ok := d.known[info.Digest]; ok {
		return true, info, nil
	}
	return d.inner.TryReusingBlob(ctx, info, cache, canSubstitute)
}

// -- All remaining ImageDestination methods delegate verbatim to d.inner. --

func (d *KnownBlobsDest) Reference() types.ImageReference { return d.inner.Reference() }
func (d *KnownBlobsDest) Close() error                    { return d.inner.Close() }
func (d *KnownBlobsDest) SupportedManifestMIMETypes() []string {
	return d.inner.SupportedManifestMIMETypes()
}
func (d *KnownBlobsDest) SupportsSignatures(ctx context.Context) error {
	return d.inner.SupportsSignatures(ctx)
}
func (d *KnownBlobsDest) DesiredLayerCompression() types.LayerCompression {
	return d.inner.DesiredLayerCompression()
}
func (d *KnownBlobsDest) AcceptsForeignLayerURLs() bool { return d.inner.AcceptsForeignLayerURLs() }
func (d *KnownBlobsDest) MustMatchRuntimeOS() bool      { return d.inner.MustMatchRuntimeOS() }
func (d *KnownBlobsDest) IgnoresEmbeddedDockerReference() bool {
	return d.inner.IgnoresEmbeddedDockerReference()
}
func (d *KnownBlobsDest) HasThreadSafePutBlob() bool { return d.inner.HasThreadSafePutBlob() }

func (d *KnownBlobsDest) PutBlob(
	ctx context.Context,
	stream io.Reader,
	inputInfo types.BlobInfo,
	cache types.BlobInfoCache,
	isConfig bool,
) (types.BlobInfo, error) {
	return d.inner.PutBlob(ctx, stream, inputInfo, cache, isConfig)
}

func (d *KnownBlobsDest) PutManifest(ctx context.Context, mf []byte, instanceDigest *digest.Digest) error {
	return d.inner.PutManifest(ctx, mf, instanceDigest)
}
func (d *KnownBlobsDest) PutSignatures(ctx context.Context, sigs [][]byte, instanceDigest *digest.Digest) error {
	return d.inner.PutSignatures(ctx, sigs, instanceDigest)
}
func (d *KnownBlobsDest) Commit(ctx context.Context, unparsedToplevel types.UnparsedImage) error {
	return d.inner.Commit(ctx, unparsedToplevel)
}

// Compile-time assertion that KnownBlobsDest satisfies the full interface.
var _ types.ImageDestination = (*KnownBlobsDest)(nil)
