package importer

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.podman.io/image/v5/directory"
	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"
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

	if err := ensureOutputIsDirectory(opts.OutputPath); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.OutputPath, 0o755); err != nil {
		return fmt.Errorf("mkdir output %s: %w", opts.OutputPath, err)
	}

	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}

	resolvedByName := make(map[string]resolvedBaseline, len(resolved))
	for _, r := range resolved {
		resolvedByName[r.Name] = r
	}

	imported := 0
	skipped := make([]string, 0)
	for _, img := range bundle.sidecar.Images {
		rb, ok := resolvedByName[img.Name]
		if !ok {
			fmt.Fprintf(progress, "%s: skipped (no baseline provided)\n", img.Name)
			skipped = append(skipped, img.Name)
			continue
		}
		if err := composeImage(ctx, img, bundle, rb,
			opts.OutputPath, opts.OutputFormat, opts.AllowConvert); err != nil {
			return err
		}
		imported++
	}
	fmt.Fprintf(progress, "imported %d of %d images; skipped: %v\n",
		imported, len(bundle.sidecar.Images), skipped)
	return nil
}

func ensureOutputIsDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat output %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"OUTPUT %s must be a directory (bundle output is written to OUTPUT/<name>.tar or OUTPUT/<name>/)",
			path)
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
