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

// composeImage imports a single resolved image into outputDir/<name>.tar
// (for archive formats) or outputDir/<name>/ (for dir format). It streams
// blobs via bundleImageSource — no tmpdir materialization.
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	outputDir, outputFormat string,
	allowConvert bool,
) error {
	baseSrc, err := rb.Ref.NewImageSource(ctx, nil)
	if err != nil {
		return fmt.Errorf("open baseline source for %q: %w", img.Name, err)
	}
	defer baseSrc.Close()

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
		baseline:     baseSrc,
		imageName:    img.Name,
	}
	src.ref = &staticSourceRef{src: src}

	resolvedFmt, err := resolveOutputFormat(outputFormat, img.Target.MediaType, allowConvert)
	if err != nil {
		return err
	}

	return copyBundleToOutput(ctx, src, outputDir, img.Name, resolvedFmt)
}

func copyBundleToOutput(
	ctx context.Context,
	src *bundleImageSource,
	outputDir, imgName, resolvedFmt string,
) error {
	var outPath string
	switch resolvedFmt {
	case FormatDir:
		outPath = filepath.Join(outputDir, imgName)
	case FormatDockerArchive, FormatOCIArchive:
		outPath = filepath.Join(outputDir, imgName+".tar")
	default:
		return fmt.Errorf("unknown --output-format %q", resolvedFmt)
	}

	outRef, err := buildOutputRef(outPath, resolvedFmt)
	if err != nil {
		return err
	}
	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer func() { _ = policyCtx.Destroy() }()

	copyOpts := &copy.Options{}
	if resolvedFmt == FormatDir {
		copyOpts.PreserveDigests = true
	}
	if _, err := copy.Image(ctx, policyCtx, outRef, src.ref, copyOpts); err != nil {
		if resolvedFmt == FormatDir {
			_ = os.RemoveAll(outPath)
		} else {
			_ = os.Remove(outPath)
		}
		return fmt.Errorf("compose %q: %w", imgName, err)
	}
	return nil
}
