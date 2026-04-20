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

	sidecarBytes, err := archive.Extract(opts.DeltaPath, tmpDir)
	if err != nil {
		return fmt.Errorf("extract delta: %w", err)
	}
	sidecar, err := diff.ParseSidecar(sidecarBytes)
	if err != nil {
		return fmt.Errorf("parse sidecar: %w", err)
	}

	deltaRef, err := directory.NewReference(tmpDir)
	if err != nil {
		return fmt.Errorf("open delta dir: %w", err)
	}
	deltaSrc, err := deltaRef.NewImageSource(ctx, nil)
	if err != nil {
		return fmt.Errorf("open delta source: %w", err)
	}
	// Delta source is owned by the composite; do NOT defer close here.

	baselineSrc, err := opts.BaselineRef.NewImageSource(ctx, nil)
	if err != nil {
		_ = deltaSrc.Close()
		return fmt.Errorf("open baseline source: %w", err)
	}
	// Baseline source is owned by the composite; do NOT defer close here.

	if err := probeBaseline(ctx, baselineSrc, sidecar); err != nil {
		_ = deltaSrc.Close()
		_ = baselineSrc.Close()
		return err
	}

	composite := NewCompositeSource(deltaSrc, baselineSrc)
	// composite.Close() will close both inner sources.
	srcRef := &compositeRef{inner: deltaRef, composite: composite}

	tmpOut := opts.OutputPath + ".tmp"
	outRef, err := buildOutputRef(tmpOut, opts.OutputFormat)
	if err != nil {
		_ = composite.Close()
		return err
	}

	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		_ = composite.Close()
		return err
	}
	defer policyCtx.Destroy()

	copyOpts := &copy.Options{}
	// PreserveDigests is only safe when writing to dir format (bit-exact pass-through).
	// For other formats (docker-archive, oci-archive), containers-image must rewrite
	// the manifest media type; PreserveDigests=true would cause copy.Image to refuse.
	if opts.OutputFormat == "dir" {
		copyOpts.PreserveDigests = true
	}

	if _, err := copy.Image(ctx, policyCtx, outRef, srcRef, copyOpts); err != nil {
		_ = composite.Close()
		return fmt.Errorf("copy composite into output: %w", err)
	}
	if err := composite.Close(); err != nil {
		return fmt.Errorf("close composite: %w", err)
	}

	if err := os.Rename(tmpOut, opts.OutputPath); err != nil {
		return fmt.Errorf("rename output: %w", err)
	}

	return verifyImport(opts, sidecar)
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
