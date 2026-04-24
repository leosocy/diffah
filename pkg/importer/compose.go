package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

const (
	FormatDockerArchive = "docker-archive"
	FormatOCIArchive    = "oci-archive"
	FormatDir           = "dir"
)

// Known single-image manifest media types. Manifest lists are rejected
// upstream by the exporter, so these two cover every sidecar we can see.
const (
	mimeDockerSchema2 = "application/vnd.docker.distribution.manifest.v2+json"
	mimeOCIManifest   = "application/vnd.oci.image.manifest.v1+json"
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
		data, err := s.fetchVerifiedBaselineBlob(ctx, info.Digest, cache)
		if err != nil {
			return nil, 0, fmt.Errorf("baseline serve %s: %w", info.Digest, err)
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
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
		return nil, 0, &diff.ErrShippedBlobDigestMismatch{
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
	out, err := zstdpatch.Decode(ctx, baseBytes, patchBytes)
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

// staticSourceRef wraps a prebuilt ImageSource so copy.Image can consume it
// as a source. The inner ref is synthetic (we don't read from the filesystem
// through the ref itself — only through the source).
type staticSourceRef struct {
	src *bundleImageSource
}

// bundleTransport is a minimal types.ImageTransport implementation used so
// that copy.Image's internal wrapper can call Transport().Name() without
// panicking on a nil Transport.
type bundleTransport struct{}

func (bundleTransport) Name() string { return "bundle" }
func (bundleTransport) ParseReference(_ string) (types.ImageReference, error) {
	return nil, fmt.Errorf("bundleTransport.ParseReference not supported")
}
func (bundleTransport) ValidatePolicyConfigurationScope(_ string) error { return nil }

func (r *staticSourceRef) Transport() types.ImageTransport         { return bundleTransport{} }
func (r *staticSourceRef) StringWithinTransport() string           { return "bundle://" + r.src.imageName }
func (r *staticSourceRef) DockerReference() reference.Named        { return nil }
func (r *staticSourceRef) PolicyConfigurationIdentity() string     { return "" }
func (r *staticSourceRef) PolicyConfigurationNamespaces() []string { return nil }
func (r *staticSourceRef) NewImage(
	_ context.Context, _ *types.SystemContext,
) (types.ImageCloser, error) {
	return nil, fmt.Errorf("staticSourceRef.NewImage not supported")
}
func (r *staticSourceRef) NewImageSource(
	_ context.Context, _ *types.SystemContext,
) (types.ImageSource, error) {
	return r.src, nil
}
func (r *staticSourceRef) NewImageDestination(
	_ context.Context, _ *types.SystemContext,
) (types.ImageDestination, error) {
	return nil, fmt.Errorf("staticSourceRef.NewImageDestination not supported")
}
func (r *staticSourceRef) DeleteImage(_ context.Context, _ *types.SystemContext) error {
	return fmt.Errorf("staticSourceRef.DeleteImage not supported")
}

// composeImage assembles a single image from bundle blobs + baseline and
// streams the result to destRef via copy.Image. rb.Src must already be open —
// this function does not open a new baseline source and does not close rb.Src.
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
	allowConvert bool,
	_ progress.Reporter, // reserved for Phase 2 progress wiring; not used yet
) error {
	mfPath := filepath.Join(bundle.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	if err != nil {
		return fmt.Errorf("read target manifest %s: %w", img.Target.ManifestDigest, err)
	}

	src := &bundleImageSource{
		blobDir:      bundle.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      bundle.sidecar,
		baseline:     rb.Src, // already open — DO NOT open a fresh one
		imageName:    img.Name,
	}
	src.ref = &staticSourceRef{src: src}

	if err := enforceOutputCompat(destRef, src, allowConvert); err != nil {
		return err
	}

	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer func() { _ = policyCtx.Destroy() }()

	copyOpts := &copy.Options{
		SourceCtx:      sysctx,
		DestinationCtx: sysctx,
		ReportWriter:   io.Discard,
	}
	if destRef.Transport().Name() == FormatDir {
		copyOpts.PreserveDigests = true
	}
	if _, err := copy.Image(ctx, policyCtx, destRef, src.Reference(), copyOpts); err != nil {
		return fmt.Errorf("copy to %s: %w", destRef.StringWithinTransport(),
			diff.ClassifyRegistryErr(err, destRef.StringWithinTransport()))
	}
	return nil
}

// enforceOutputCompat rejects a destination transport + source manifest
// combination that would require cross-format conversion, unless
// allowConvert was explicitly set.
func enforceOutputCompat(dest types.ImageReference, src types.ImageSource, allowConvert bool) error {
	if allowConvert {
		return nil
	}
	_, mime, err := src.GetManifest(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("read assembled manifest: %w", err)
	}
	if mime == "" {
		return nil
	}
	switch dest.Transport().Name() {
	case FormatDockerArchive:
		if mime != mimeDockerSchema2 {
			return &diff.ErrIncompatibleOutputFormat{SourceMime: mime, OutputFormat: FormatDockerArchive}
		}
	case FormatOCIArchive, "oci":
		if mime != mimeOCIManifest {
			return &diff.ErrIncompatibleOutputFormat{SourceMime: mime, OutputFormat: dest.Transport().Name()}
		}
		// dir: always permitted — dir transport copies blobs byte-for-byte regardless of manifest media type.
		// docker:// and other registry transports — upstream copy.Image handles manifest conversion.
	}
	return nil
}
