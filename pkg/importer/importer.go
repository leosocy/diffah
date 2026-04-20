package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/directory"
	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/manifest"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

// Options carries all inputs to Import.
type Options struct {
	DeltaPath    string
	BaselineRef  types.ImageReference
	OutputPath   string
	OutputFormat string // "docker-archive" | "oci-archive" | "dir"
}

// Import performs the full import pipeline described in spec §8.
func Import(ctx context.Context, opts Options) error {
	tmpDir, err := os.MkdirTemp("", "diffah-import-")
	if err != nil {
		return fmt.Errorf("create tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sidecar, err := extractSidecar(opts.DeltaPath, tmpDir)
	if err != nil {
		return err
	}

	composite, srcRef, err := openCompositeSource(ctx, tmpDir, opts.BaselineRef, sidecar)
	if err != nil {
		return err
	}

	tmpOut := opts.OutputPath + ".tmp"
	if err := runCopy(ctx, srcRef, tmpOut, opts.OutputFormat); err != nil {
		_ = composite.Close()
		_ = removeOutput(tmpOut, opts.OutputFormat)
		return err
	}
	if err := composite.Close(); err != nil {
		return fmt.Errorf("close composite: %w", err)
	}

	if err := os.Rename(tmpOut, opts.OutputPath); err != nil {
		return fmt.Errorf("rename output: %w", err)
	}
	return verifyImport(opts, sidecar)
}

// extractSidecar unpacks the delta archive into tmpDir and parses the sidecar.
func extractSidecar(deltaPath, tmpDir string) (*diff.Sidecar, error) {
	raw, err := archive.Extract(deltaPath, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("extract delta: %w", err)
	}
	sidecar, err := diff.ParseSidecar(raw)
	if err != nil {
		return nil, fmt.Errorf("parse sidecar: %w", err)
	}
	return sidecar, nil
}

// openCompositeSource opens both the delta (directory:) and baseline sources,
// runs the fail-fast probe, and returns the composite wrapped in a reference
// adapter. The caller owns composite.Close() — do not close the inner sources.
func openCompositeSource(ctx context.Context, tmpDir string, baselineRef types.ImageReference, sidecar *diff.Sidecar) (*CompositeSource, types.ImageReference, error) {
	deltaRef, err := directory.NewReference(tmpDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open delta dir: %w", err)
	}
	deltaSrc, err := deltaRef.NewImageSource(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("open delta source: %w", err)
	}
	baselineSrc, err := baselineRef.NewImageSource(ctx, nil)
	if err != nil {
		_ = deltaSrc.Close()
		return nil, nil, fmt.Errorf("open baseline source: %w", err)
	}
	if err := probeBaseline(ctx, baselineSrc, sidecar); err != nil {
		_ = deltaSrc.Close()
		_ = baselineSrc.Close()
		return nil, nil, err
	}
	composite := NewCompositeSource(deltaSrc, baselineSrc)
	return composite, &compositeRef{inner: deltaRef, composite: composite}, nil
}

// runCopy builds the output reference, configures copy.Options, and invokes
// copy.Image. PreserveDigests is only set for dir output — other formats must
// rewrite manifest media types, which copy.Image refuses if PreserveDigests is
// true.
func runCopy(ctx context.Context, srcRef types.ImageReference, tmpOut, format string) error {
	outRef, err := buildOutputRef(tmpOut, format)
	if err != nil {
		return err
	}
	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer policyCtx.Destroy()

	copyOpts := &copy.Options{}
	if format == "dir" {
		copyOpts.PreserveDigests = true
	}
	if _, err := copy.Image(ctx, policyCtx, outRef, srcRef, copyOpts); err != nil {
		return fmt.Errorf("copy composite into output: %w", err)
	}
	return nil
}

// removeOutput cleans up a partial .tmp output left by a failed copy.Image.
func removeOutput(path, format string) error {
	if format == "dir" {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// probeBaseline verifies that every digest listed in sidecar.RequiredFromBaseline
// is present among the baseline's manifest layers. Returns
// diff.ErrBaselineMissingBlob on the first missing digest.
func probeBaseline(ctx context.Context, src types.ImageSource, s *diff.Sidecar) error {
	if len(s.RequiredFromBaseline) == 0 {
		return nil
	}
	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return fmt.Errorf("read baseline manifest: %w", err)
	}
	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return fmt.Errorf("parse baseline manifest: %w", err)
	}
	have := make(map[digest.Digest]struct{}, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		have[l.Digest] = struct{}{}
	}
	for _, req := range s.RequiredFromBaseline {
		if _, ok := have[req.Digest]; !ok {
			return &diff.ErrBaselineMissingBlob{
				Digest: string(req.Digest),
				Source: src.Reference().StringWithinTransport(),
			}
		}
	}
	return nil
}

// buildOutputRef creates a types.ImageReference for the chosen format.
func buildOutputRef(path, format string) (types.ImageReference, error) {
	switch format {
	case "docker-archive", "":
		named, err := dockerref.ParseNormalizedNamed("diffah-import:latest")
		if err != nil {
			return nil, fmt.Errorf("build docker ref: %w", err)
		}
		nt, ok := named.(dockerref.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("docker ref not NamedTagged")
		}
		return dockerarchive.NewReference(path, nt)
	case "oci-archive":
		return ociarchive.NewReference(path, "diffah-import:latest")
	case "dir":
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", path, err)
		}
		return directory.NewReference(path)
	default:
		return nil, fmt.Errorf("unknown --output-format %q", format)
	}
}

// verifyImport sanity-checks the produced output for dir format by comparing
// the written manifest digest against the sidecar's target manifest digest.
// For other formats, copy.Image's internal validation is trusted.
func verifyImport(opts Options, sidecar *diff.Sidecar) error {
	if opts.OutputFormat != "dir" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(opts.OutputPath, "manifest.json"))
	if err != nil {
		return fmt.Errorf("verify read manifest: %w", err)
	}
	got := digest.FromBytes(raw)
	if got != sidecar.Target.ManifestDigest {
		return &diff.ErrDigestMismatch{
			Where: "post-import dir manifest",
			Want:  string(sidecar.Target.ManifestDigest),
			Got:   string(got),
		}
	}
	return nil
}

// DryRunReport summarizes a dry-run import: which required baseline blobs
// are reachable and which (if any) are missing.
type DryRunReport struct {
	AllReachable   bool
	MissingDigests []string
	RequiredBlobs  int
	BaselineSource string
}

// DryRun performs steps 1-4 of the import pipeline (extract, parse, open
// baseline, probe) without calling copy.Image or writing any output files.
// Returns a report describing whether every required baseline blob is
// reachable from the provided baseline reference.
func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	tmpDir, err := os.MkdirTemp("", "diffah-import-dryrun-")
	if err != nil {
		return DryRunReport{}, fmt.Errorf("create tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sidecar, err := extractSidecar(opts.DeltaPath, tmpDir)
	if err != nil {
		return DryRunReport{}, err
	}

	baselineSrc, err := opts.BaselineRef.NewImageSource(ctx, nil)
	if err != nil {
		return DryRunReport{}, fmt.Errorf("open baseline source: %w", err)
	}
	defer baselineSrc.Close()

	report := DryRunReport{
		RequiredBlobs:  len(sidecar.RequiredFromBaseline),
		BaselineSource: baselineSrc.Reference().StringWithinTransport(),
	}

	raw, mime, err := baselineSrc.GetManifest(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("read baseline manifest: %w", err)
	}
	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return report, fmt.Errorf("parse baseline manifest: %w", err)
	}
	have := make(map[digest.Digest]struct{}, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		have[l.Digest] = struct{}{}
	}
	for _, req := range sidecar.RequiredFromBaseline {
		if _, ok := have[req.Digest]; !ok {
			report.MissingDigests = append(report.MissingDigests, string(req.Digest))
		}
	}
	report.AllReachable = len(report.MissingDigests) == 0
	return report, nil
}

// compositeRef wraps a directory reference so copy.Image receives our
// CompositeSource instead of the plain directory: source.
type compositeRef struct {
	inner     types.ImageReference
	composite *CompositeSource
}

func (r *compositeRef) Transport() types.ImageTransport { return r.inner.Transport() }
func (r *compositeRef) StringWithinTransport() string   { return r.inner.StringWithinTransport() }
func (r *compositeRef) DockerReference() dockerref.Named {
	return r.inner.DockerReference()
}
func (r *compositeRef) PolicyConfigurationIdentity() string {
	return r.inner.PolicyConfigurationIdentity()
}
func (r *compositeRef) PolicyConfigurationNamespaces() []string {
	return r.inner.PolicyConfigurationNamespaces()
}
func (r *compositeRef) NewImage(ctx context.Context, sys *types.SystemContext) (types.ImageCloser, error) {
	return r.inner.NewImage(ctx, sys)
}
func (r *compositeRef) NewImageSource(_ context.Context, _ *types.SystemContext) (types.ImageSource, error) {
	return noCloseSource{CompositeSource: r.composite}, nil
}
func (r *compositeRef) NewImageDestination(ctx context.Context, sys *types.SystemContext) (types.ImageDestination, error) {
	return r.inner.NewImageDestination(ctx, sys)
}
func (r *compositeRef) DeleteImage(ctx context.Context, sys *types.SystemContext) error {
	return r.inner.DeleteImage(ctx, sys)
}

// Compile-time assertion that compositeRef satisfies types.ImageReference.
var _ types.ImageReference = (*compositeRef)(nil)

// noCloseSource wraps CompositeSource so copy.Image's own Close() doesn't
// trigger Close on the composite prematurely — Import controls closing
// explicitly after copy.Image returns.
type noCloseSource struct {
	*CompositeSource
}

func (noCloseSource) Close() error { return nil }
