package importer

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/directory"
	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
)

const (
	FormatDockerArchive = "docker-archive"
	FormatOCIArchive    = "oci-archive"
	FormatDir           = "dir"
)

type Options struct {
	DeltaPath    string
	Baselines    map[string]string
	Strict       bool
	OutputPath   string
	OutputFormat string
	AllowConvert bool
	Progress     io.Writer
}

func Import(ctx context.Context, opts Options) error {
	return fmt.Errorf("bundle import not yet wired in this commit")
}

func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	return DryRunReport{}, fmt.Errorf("bundle dry-run not yet wired in this commit")
}

type DryRunReport struct {
	TotalImages  int
	TotalBlobs   int
	ArchiveSize  int64
	PerImage     []ImageDryRunStats
	MissingNames []string
}

type ImageDryRunStats struct {
	Name          string
	ShippedBlobs  int
	RequiredBlobs int
	ArchiveSize   int64
}

func runCopy(ctx context.Context, srcRef types.ImageReference, tmpOut, format string) error {
	outRef, err := buildOutputRef(tmpOut, format)
	if err != nil {
		return err
	}
	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer func() { _ = policyCtx.Destroy() }()

	copyOpts := &copy.Options{}
	if format == FormatDir {
		copyOpts.PreserveDigests = true
	}
	if _, err := copy.Image(ctx, policyCtx, outRef, srcRef, copyOpts); err != nil {
		return fmt.Errorf("copy composite into output: %w", err)
	}
	return nil
}

func removeOutput(path, format string) error {
	if format == FormatDir {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func buildOutputRef(path, format string) (types.ImageReference, error) {
	switch format {
	case FormatDockerArchive, "":
		named, err := dockerref.ParseNormalizedNamed("diffah-import:latest")
		if err != nil {
			return nil, fmt.Errorf("build docker ref: %w", err)
		}
		nt, ok := named.(dockerref.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("docker ref not NamedTagged")
		}
		return dockerarchive.NewReference(path, nt)
	case FormatOCIArchive:
		return ociarchive.NewReference(path, "diffah-import:latest")
	case FormatDir:
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", path, err)
		}
		return directory.NewReference(path)
	default:
		return nil, fmt.Errorf("unknown --output-format %q", format)
	}
}

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
func (r *compositeRef) NewImageDestination(
	ctx context.Context, sys *types.SystemContext,
) (types.ImageDestination, error) {
	return r.inner.NewImageDestination(ctx, sys)
}
func (r *compositeRef) DeleteImage(ctx context.Context, sys *types.SystemContext) error {
	return r.inner.DeleteImage(ctx, sys)
}

var _ types.ImageReference = (*compositeRef)(nil)

type noCloseSource struct {
	*CompositeSource
}

func (noCloseSource) Close() error { return nil }
