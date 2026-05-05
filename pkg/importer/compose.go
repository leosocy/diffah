package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
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
// verified before return.
//
// Streaming I/O (PR4): serveFull returns a path-backed *verifyingReadCloser,
// servePatch shells out to zstdpatch.DecodeStream and returns a
// *verifyingReadCloser around the scratch file (cleaned up on Close).
// workdir is the per-Import scratch root (parent: <workdir>/scratch/<image>).
type bundleImageSource struct {
	blobDir      string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
	imageName    string
	ref          types.ImageReference
	spool        *BaselineSpool
	workdir      string
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

// HasThreadSafeGetBlob returns true unconditionally as of PR5.
//
// PR3 spool (singleflight + atomic rename) + PR4 per-call scratch
// CreateTemp suffix + PR4 path-backed verifyingReadCloser make both
// serveFull and servePatch safe under concurrent same-digest GetBlob
// within the same image source. PR5's importEachImage drives parallel
// image applies through copy.Image which now relies on this flag.
//
// Note: this does NOT delegate to s.baseline.HasThreadSafeGetBlob().
// copy.Image consults this flag *per-source* — when the bundle source
// returns true, it may concurrently call GetBlob on the bundle, but the
// underlying baseline calls (made through fetchVerifiedBaselineBlob and
// the spool fetch closure) are still serialized by BaselineSpool's
// singleflight Do — only one underlying baseline GetBlob runs at a time
// per digest, regardless of the baseline's own thread-safety.
func (s *bundleImageSource) HasThreadSafeGetBlob() bool {
	return true
}

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
			if isBlobNotFound(err) {
				return nil, 0, &ErrMissingBaselineReuseLayer{
					ImageName:   s.imageName,
					LayerDigest: info.Digest,
				}
			}
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

// serveFull streams a shipped full-encoded blob from disk. Returns a
// *verifyingReadCloser that hashes bytes during Read and surfaces
// *diff.ErrShippedBlobDigestMismatch on EOF if the running sha256 does
// not match d. The reader owns the *os.File; closing it closes the file.
func (s *bundleImageSource) serveFull(d digest.Digest) (io.ReadCloser, int64, error) {
	path := filepath.Join(s.blobDir, d.Algorithm().String(), d.Encoded())
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open full blob %s: %w", d, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("stat full blob %s: %w", d, err)
	}
	return &verifyingReadCloser{
		f: f, expected: d, hasher: sha256.New(),
		imageName: s.imageName, kind: kindShipped,
	}, st.Size(), nil
}

// servePatch reconstructs a patched blob via zstdpatch.DecodeStream and
// returns a *verifyingReadCloser around the scratch output file. The
// patch sits at <blobDir>/<algo>/<digest> from the bundle extraction;
// the patch-from baseline is materialized through the per-Import spool
// (singleflight + atomic rename + digest-verify) and consumed directly
// via DecodeStream — no in-memory buffering.
//
// The cache parameter is forwarded into the baseline GetBlob fetch
// closure unchanged: the docker registry transport requires a non-nil
// types.BlobInfoCache to construct its per-blob lookup state, so passing
// nil here panics inside containers-image. The spool dedups by digest
// and is unrelated to this cache.
//
// Errors:
//   - baseline blob-not-found → *ErrMissingPatchSource (B1, CategoryContent)
//   - baseline digest mismatch → *diff.ErrBaselineBlobDigestMismatch
//     (re-wrapped with ImageName, mirroring fetchVerifiedBaselineBlob)
//   - decode failure → fmt.Errorf wrap
//   - on-EOF digest mismatch → *diff.ErrIntraLayerAssemblyMismatch
//     (raised from verifyingReadCloser.Read at io.EOF)
func (s *bundleImageSource) servePatch(
	ctx context.Context, target digest.Digest, entry diff.BlobEntry, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	patchPath := filepath.Join(s.blobDir, target.Algorithm().String(), target.Encoded())
	refPath, err := s.spool.GetOrSpool(ctx, entry.PatchFromDigest, func() (io.ReadCloser, error) {
		rc, _, gerr := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: entry.PatchFromDigest}, cache)
		return rc, gerr
	})
	if err != nil {
		if isBlobNotFound(err) {
			return nil, 0, &ErrMissingPatchSource{
				ImageName:       s.imageName,
				ShippedDigest:   target,
				PatchFromDigest: entry.PatchFromDigest,
			}
		}
		var mismatch *diff.ErrBaselineBlobDigestMismatch
		if errors.As(err, &mismatch) && mismatch.ImageName == "" {
			mismatch.ImageName = s.imageName
		}
		return nil, 0, fmt.Errorf("baseline spool %s: %w", entry.PatchFromDigest, err)
	}

	scratchDir := filepath.Join(s.workdir, "scratch", s.imageName)
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return nil, 0, fmt.Errorf("mkdir scratch for %s: %w", target, err)
	}
	// Per-call uniqueness via CreateTemp so two concurrent GetBlob() for
	// the same target digest don't race on a shared output path. The
	// upstream copier (copy.Image) is currently serial per manifest, but
	// HasThreadSafeGetBlob already delegates true for registry baselines,
	// so any future caller that fans out same-image blobs in parallel
	// would otherwise truncate-while-reading. Per-call temps remove the
	// hazard entirely; verifyingReadCloser.Close removes its own temp.
	tmp, err := os.CreateTemp(scratchDir, target.Encoded()+"-*")
	if err != nil {
		return nil, 0, fmt.Errorf("create scratch tmp for %s: %w", target, err)
	}
	scratchPath := tmp.Name()
	_ = tmp.Close() // DecodeStream will rewrite the file
	var committed bool
	defer func() {
		// On any post-DecodeStream failure (Open, Stat) the scratch file
		// is no longer reachable through verifyingReadCloser.Close, so we
		// remove it here. The successful path returns before this deferred
		// closure has anything to do (committed = true).
		if !committed {
			_ = os.Remove(scratchPath)
		}
	}()

	if _, err := zstdpatch.DecodeStream(ctx, refPath, patchPath, scratchPath); err != nil {
		return nil, 0, fmt.Errorf("decode patch for %s: %w", target, err)
	}
	f, err := os.Open(scratchPath)
	if err != nil {
		return nil, 0, fmt.Errorf("open decoded %s: %w", target, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("stat decoded %s: %w", target, err)
	}
	committed = true
	return &verifyingReadCloser{
		f: f, expected: target, hasher: sha256.New(),
		imageName: s.imageName, kind: kindAssembled, scratchPath: scratchPath,
	}, st.Size(), nil
}

// readerKind discriminates which sentinel verifyingReadCloser raises on a
// post-Read digest mismatch: kindShipped for full bundle blobs (-> B-class
// shipped-blob mismatch), kindAssembled for patch-decode outputs
// (-> intra-layer assembly mismatch).
type readerKind int

const (
	kindShipped readerKind = iota
	kindAssembled
)

// verifyingReadCloser wraps an *os.File, accumulating sha256 on each Read.
// At io.EOF it compares the running digest to expected and surfaces
// *diff.ErrShippedBlobDigestMismatch (kindShipped) or
// *diff.ErrIntraLayerAssemblyMismatch (kindAssembled) on mismatch. Close
// closes the underlying file and (for kindAssembled) removes the scratch
// path.
type verifyingReadCloser struct {
	f           *os.File
	expected    digest.Digest
	hasher      hash.Hash
	imageName   string
	kind        readerKind
	scratchPath string // non-empty iff kind == kindAssembled
}

func (r *verifyingReadCloser) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	if n > 0 {
		_, _ = r.hasher.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		got := digest.NewDigestFromBytes(digest.SHA256, r.hasher.Sum(nil))
		if got != r.expected {
			return n, r.mismatchErr(got)
		}
	}
	return n, err
}

func (r *verifyingReadCloser) mismatchErr(got digest.Digest) error {
	switch r.kind {
	case kindShipped:
		return &diff.ErrShippedBlobDigestMismatch{
			ImageName: r.imageName, Digest: r.expected.String(), Got: got.String(),
		}
	case kindAssembled:
		return &diff.ErrIntraLayerAssemblyMismatch{
			Digest: r.expected.String(), Got: got.String(),
		}
	}
	// Defensive: a future kind addition that forgets to wire a sentinel
	// must not silently downgrade a digest mismatch to "no error".
	return fmt.Errorf("verifyingReadCloser: unknown kind %d for %s (got %s)",
		r.kind, r.expected, got)
}

func (r *verifyingReadCloser) Close() error {
	err := r.f.Close()
	if r.scratchPath != "" {
		_ = os.Remove(r.scratchPath)
	}
	return err
}

// fetchVerifiedBaselineBlob reads `d` from the wrapped baseline source and
// verifies its digest. Used for blobs the sidecar did not ship (the
// baseline-only-reuse path in GetBlob). Routed through s.spool so each
// distinct digest is fetched at most once per Import() call regardless of
// how many images in a multi-image bundle share it; the on-disk spool also
// keeps RSS bounded vs the previous in-memory cache.
//
// The cache types.BlobInfoCache parameter is the upstream containers-image
// blob info cache and is forwarded to s.baseline.GetBlob unchanged — it is
// a separate concern from s.spool.
//
// PR4 streamed servePatch directly off the spool path (no longer going
// through this helper). The remaining caller is the baseline-delegate
// branch in GetBlob, which still returns []byte; that branch is the next
// streaming candidate but is left as-is until a later PR.
//
// Re-wraps any *diff.ErrBaselineBlobDigestMismatch returned by the spool
// to repopulate ImageName: the spool is per-Import (image-agnostic) but
// the operator-facing error has historically named the offending image
// so support flows can locate the apply context.
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
	ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) ([]byte, error) {
	path, err := s.spool.GetOrSpool(ctx, d, func() (io.ReadCloser, error) {
		rc, _, gerr := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, cache)
		return rc, gerr
	})
	if err != nil {
		var mismatch *diff.ErrBaselineBlobDigestMismatch
		if errors.As(err, &mismatch) && mismatch.ImageName == "" {
			mismatch.ImageName = s.imageName
		}
		return nil, err
	}
	return os.ReadFile(path)
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
//
// workdir is the per-Import scratch root (parent: <workdir>/scratch/<image>
// for patch decoding); cleanup is owned by Import via the workdir defer.
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
	allowConvert bool,
	spool *BaselineSpool,
	workdir string,
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
		spool:        spool,
		workdir:      workdir,
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
