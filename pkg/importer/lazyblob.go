package importer

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/pkg/blobinfocache"
	"go.podman.io/image/v5/types"
)

// lazyBlobFetcher issues on-demand GetBlob calls against a held-open
// ImageSource. Used by composeImage to pull baseline layers only
// when the delta requires them, avoiding a full-image preload.
type lazyBlobFetcher struct {
	src   types.ImageSource
	cache types.BlobInfoCache
}

func newLazyBlobFetcher(src types.ImageSource) *lazyBlobFetcher {
	return &lazyBlobFetcher{
		src:   src,
		cache: blobinfocache.DefaultCache(nil),
	}
}

// Fetch returns the full byte content of the blob identified by d.
// Callers own the returned slice; repeated calls for the same digest
// re-fetch (no local caching beyond the upstream BlobInfoCache).
func (f *lazyBlobFetcher) Fetch(ctx context.Context, d digest.Digest) ([]byte, error) {
	rc, _, err := f.src.GetBlob(ctx, types.BlobInfo{Digest: d}, f.cache)
	if err != nil {
		return nil, fmt.Errorf("fetch baseline blob %s: %w", d, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
