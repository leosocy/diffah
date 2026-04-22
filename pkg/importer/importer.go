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
	"github.com/leosocy/diffah/pkg/diff"
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
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return err
	}
	defer bundle.cleanup()

	if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
		return err
	}

	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return err
	}

	if len(resolved) == 0 {
		return fmt.Errorf("no baselines resolved; at least one is required for import")
	}

	if len(bundle.sidecar.Images) > 1 {
		return fmt.Errorf("multi-image bundle import is not yet supported; specify a single image to import")
	}

	rb := resolved[0]
	var img diff.ImageEntry
	for _, i := range bundle.sidecar.Images {
		if i.Name == rb.Name {
			img = i
			break
		}
	}

	ci, err := composeImageLegacy(ctx, img, bundle.sidecar, bundle, rb.Ref)
	if err != nil {
		return fmt.Errorf("compose image %q: %w", rb.Name, err)
	}
	defer ci.cleanup()

	resolvedFmt, err := resolveOutputFormat(opts.OutputFormat, img.Target.MediaType, opts.AllowConvert)
	if err != nil {
		return err
	}

	tmpOut := opts.OutputPath + ".tmp"
	if err := runCopy(ctx, ci.Ref, tmpOut, resolvedFmt); err != nil {
		_ = removeOutput(tmpOut, resolvedFmt)
		return fmt.Errorf("copy image %q: %w", rb.Name, err)
	}
	if err := os.Rename(tmpOut, opts.OutputPath); err != nil {
		return fmt.Errorf("rename output: %w", err)
	}

	return nil
}

func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return DryRunReport{}, err
	}
	defer bundle.cleanup()

	if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
		return DryRunReport{}, err
	}

	report := DryRunReport{
		TotalImages: len(bundle.sidecar.Images),
		TotalBlobs:  len(bundle.sidecar.Blobs),
	}
	for _, e := range bundle.sidecar.Blobs {
		report.ArchiveSize += e.ArchiveSize
	}

	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return report, err
	}
	resolvedNames := make(map[string]struct{}, len(resolved))
	for _, r := range resolved {
		resolvedNames[r.Name] = struct{}{}
		report.PerImage = append(report.PerImage, ImageDryRunStats{
			Name: r.Name,
		})
	}
	for _, img := range bundle.sidecar.Images {
		if _, ok := resolvedNames[img.Name]; !ok {
			report.MissingNames = append(report.MissingNames, img.Name)
		}
	}

	return report, nil
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
