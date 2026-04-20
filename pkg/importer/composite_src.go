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
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"
)

// CompositeSource implements types.ImageSource by overlaying a delta source
// on top of a baseline source. GetManifest comes from the delta (the
// authoritative holder of the target manifest). GetBlob prefers the delta
// and falls back to baseline when the delta returns a not-found error.
type CompositeSource struct {
	delta    types.ImageSource
	baseline types.ImageSource
}

// NewCompositeSource wraps the two inner sources. Close() on the composite
// calls Close on both; the caller must not close the inner sources directly.
func NewCompositeSource(delta, baseline types.ImageSource) *CompositeSource {
	return &CompositeSource{delta: delta, baseline: baseline}
}

// GetBlob serves delta blobs first, then baseline. Any error from the delta
// that is not "not found" propagates verbatim.
func (c *CompositeSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	r, size, err := c.delta.GetBlob(ctx, info, cache)
	if err == nil {
		return r, size, nil
	}
	if !isNotFound(err) {
		return nil, 0, fmt.Errorf("delta get blob %s: %w", info.Digest, err)
	}
	r, size, err = c.baseline.GetBlob(ctx, info, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("blob %s not found in delta or baseline: %w", info.Digest, err)
	}
	return r, size, nil
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

// isNotFound detects whether err represents a blob-not-found condition.
// The directory: transport wraps os.ErrNotExist when a blob file is missing.
func isNotFound(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}

// Compile-time interface check.
var _ types.ImageSource = (*CompositeSource)(nil)
