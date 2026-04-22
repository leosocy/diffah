package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

// bundleImageSource implements go.podman.io/image/v5/types.ImageSource for
// one resolved image inside a bundle. Shipped blobs come from the extracted
// bundle's blobs/ directory (decoded on the fly for encoding=patch); required
// blobs come from a wrapped baseline source. Every served blob is digest-
// verified before return. No tmpdir is ever written to — copy.Image reads
// via GetBlob, which returns in-memory bytes.
type bundleImageSource struct {
	blobDir      string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
	imageName    string
	ref          types.ImageReference
}

var _ types.ImageSource = (*bundleImageSource)(nil)

func (s *bundleImageSource) Reference() types.ImageReference { return s.ref }
func (s *bundleImageSource) Close() error                    { return nil }

func (s *bundleImageSource) GetManifest(_ context.Context, instance *digest.Digest) ([]byte, string, error) {
	if instance != nil {
		return nil, "", fmt.Errorf("instance manifest lookups not supported")
	}
	return s.manifest, s.manifestMime, nil
}

func (s *bundleImageSource) HasThreadSafeGetBlob() bool { return false }

func (s *bundleImageSource) GetSignatures(
	_ context.Context, _ *digest.Digest,
) ([][]byte, error) {
	return nil, nil
}

func (s *bundleImageSource) LayerInfosForCopy(
	_ context.Context, _ *digest.Digest,
) ([]types.BlobInfo, error) {
	return nil, nil
}

func (s *bundleImageSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	entry, ok := s.sidecar.Blobs[info.Digest]
	if !ok {
		return nil, 0, fmt.Errorf("baseline delegation not implemented yet") // TASK-5
	}
	switch entry.Encoding {
	case diff.EncodingFull:
		return s.serveFull(info.Digest)
	case diff.EncodingPatch:
		return s.servePatch(ctx, info.Digest, entry, cache)
	}
	return nil, 0, fmt.Errorf("unknown encoding %q for blob %s", entry.Encoding, info.Digest)
}

func (s *bundleImageSource) serveFull(d digest.Digest) (io.ReadCloser, int64, error) {
	path := filepath.Join(s.blobDir, d.Algorithm().String(), d.Encoded())
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read full blob %s: %w", d, err)
	}
	if got := digest.FromBytes(data); got != d {
		return nil, 0, &diff.ErrBaselineBlobDigestMismatch{
			ImageName: s.imageName, Digest: d.String(), Got: got.String(),
		}
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (s *bundleImageSource) servePatch(
	ctx context.Context, target digest.Digest, entry diff.BlobEntry, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	patchPath := filepath.Join(s.blobDir, target.Algorithm().String(), target.Encoded())
	patchBytes, err := os.ReadFile(patchPath)
	if err != nil {
		return nil, 0, fmt.Errorf("read patch blob %s: %w", target, err)
	}
	baseBytes, err := s.fetchVerifiedBaselineBlob(ctx, entry.PatchFromDigest, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch patch-from blob %s: %w", entry.PatchFromDigest, err)
	}
	out, err := zstdpatch.Decode(baseBytes, patchBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("decode patch for %s: %w", target, err)
	}
	if got := digest.FromBytes(out); got != target {
		return nil, 0, &diff.ErrIntraLayerAssemblyMismatch{
			Digest: target.String(), Got: got.String(),
		}
	}
	return io.NopCloser(bytes.NewReader(out)), int64(len(out)), nil
}

// fetchVerifiedBaselineBlob reads `d` from the wrapped baseline source and
// verifies its digest. Used both for patch-from references (Task 4) and for
// blobs the sidecar did not ship (Task 5).
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
	ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) ([]byte, error) {
	rc, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, cache)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if got := digest.FromBytes(data); got != d {
		return nil, &diff.ErrBaselineBlobDigestMismatch{
			ImageName: s.imageName, Digest: d.String(), Got: got.String(),
		}
	}
	return data, nil
}
