package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/directory"
	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/zstdpatch"
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
	Probe        func(context.Context) (bool, string)
}

func (o *Options) probeOrDefault() func(context.Context) (bool, string) {
	if o.Probe != nil {
		return o.Probe
	}
	return zstdpatch.Available
}

func Import(ctx context.Context, opts Options) error {
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return err
	}
	defer bundle.cleanup()

	if bundle.sidecar.RequiresZstd() {
		ok, reason := opts.probeOrDefault()(ctx)
		if !ok {
			return fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
		}
	}

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

	var blobStats BlobStats
	for _, b := range bundle.sidecar.Blobs {
		switch b.Encoding {
		case diff.EncodingFull:
			blobStats.FullCount++
			blobStats.FullBytes += b.ArchiveSize
		case diff.EncodingPatch:
			blobStats.PatchCount++
			blobStats.PatchBytes += b.ArchiveSize
		}
	}

	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return DryRunReport{}, err
	}

	images, err := buildImageDryRuns(bundle, resolved)
	if err != nil {
		return DryRunReport{}, err
	}

	var archiveBytes int64
	info, err := os.Stat(opts.DeltaPath)
	if err != nil {
		return DryRunReport{}, fmt.Errorf("stat delta archive %s: %w", opts.DeltaPath, err)
	}
	archiveBytes = info.Size()

	requiresZstd := bundle.sidecar.RequiresZstd()
	var zstdAvailable bool
	if requiresZstd {
		// DryRun is informational and must not fail on a missing probe —
		// callers want to know whether zstd is required, not be blocked by
		// its absence.
		zstdAvailable, _ = opts.probeOrDefault()(ctx)
	}

	return DryRunReport{
		Feature:       bundle.sidecar.Feature,
		Version:       bundle.sidecar.Version,
		Tool:          bundle.sidecar.Tool,
		ToolVersion:   bundle.sidecar.ToolVersion,
		CreatedAt:     bundle.sidecar.CreatedAt,
		Platform:      bundle.sidecar.Platform,
		Images:        images,
		Blobs:         blobStats,
		ArchiveBytes:  archiveBytes,
		RequiresZstd:  requiresZstd,
		ZstdAvailable: zstdAvailable,
	}, nil
}

func buildImageDryRuns(bundle *extractedBundle, resolved []resolvedBaseline) ([]ImageDryRun, error) {
	provided := make(map[string]struct{}, len(resolved))
	for _, r := range resolved {
		provided[r.Name] = struct{}{}
	}
	images := make([]ImageDryRun, 0, len(bundle.sidecar.Images))
	for _, img := range bundle.sidecar.Images {
		layers, err := readManifestLayers(bundle, img.Target.ManifestDigest)
		if err != nil {
			return nil, fmt.Errorf("read target manifest for %q: %w", img.Name, err)
		}
		var archCount, baseCount, patchCount int
		for _, l := range layers {
			if entry, ok := bundle.sidecar.Blobs[l]; ok {
				archCount++
				if entry.Encoding == diff.EncodingPatch {
					patchCount++
				}
			} else {
				baseCount++
			}
		}
		_, has := provided[img.Name]
		row := ImageDryRun{
			Name:                   img.Name,
			BaselineManifestDigest: img.Baseline.ManifestDigest,
			TargetManifestDigest:   img.Target.ManifestDigest,
			BaselineProvided:       has,
			WouldImport:            has,
			LayerCount:             len(layers),
			ArchiveLayerCount:      archCount,
			BaselineLayerCount:     baseCount,
			PatchLayerCount:        patchCount,
		}
		if !has {
			row.SkipReason = "no baseline provided"
		}
		images = append(images, row)
	}
	return images, nil
}

func readManifestLayers(bundle *extractedBundle, mfDigest digest.Digest) ([]digest.Digest, error) {
	path := filepath.Join(bundle.blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("manifest %s has no layers", mfDigest)
	}
	out := make([]digest.Digest, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out, nil
}

type DryRunReport struct {
	Feature       string
	Version       string
	Tool          string
	ToolVersion   string
	CreatedAt     time.Time
	Platform      string
	Images        []ImageDryRun
	Blobs         BlobStats
	ArchiveBytes  int64
	RequiresZstd  bool
	ZstdAvailable bool
}

type BlobStats struct {
	FullCount  int
	PatchCount int
	FullBytes  int64
	PatchBytes int64
}

type ImageDryRun struct {
	Name                   string
	BaselineManifestDigest digest.Digest
	TargetManifestDigest   digest.Digest
	BaselineProvided       bool
	WouldImport            bool
	SkipReason             string
	LayerCount             int
	ArchiveLayerCount      int
	BaselineLayerCount     int // layers not present in the archive (sourced from baseline)
	PatchLayerCount        int
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
