package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/oci"
	"github.com/leosocy/diffah/pkg/diff"
)

// Options carries all inputs to Export. One of BaselineRef or
// BaselineManifestPath must be set.
type Options struct {
	TargetRef            types.ImageReference
	BaselineRef          types.ImageReference
	BaselineManifestPath string
	Platform             string
	Compress             string
	OutputPath           string
	ToolVersion          string
}

// Export performs the full export pipeline described in spec §7:
//  1. Open baseline and collect its layer digests.
//  2. Copy the target image into a temp dir, skipping baseline layers via
//     KnownBlobsDest.
//  3. Build the sidecar from the written manifest + ComputePlan.
//  4. Pack the temp dir + sidecar into the output archive.
//  5. Verify the packed sidecar round-trips correctly.
func Export(ctx context.Context, opts Options) error {
	baseline, err := openBaseline(ctx, opts)
	if err != nil {
		return err
	}
	baselineDigests, err := baseline.LayerDigests(ctx)
	if err != nil {
		return fmt.Errorf("load baseline digests: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "diffah-export-")
	if err != nil {
		return fmt.Errorf("create tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := copyTargetIntoDir(ctx, opts, tmpDir, baselineDigests); err != nil {
		return err
	}

	sidecar, err := buildSidecar(tmpDir, baseline, baselineDigests, opts)
	if err != nil {
		return err
	}
	sidecarBytes, err := sidecar.Marshal()
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}

	compression := archive.CompressNone
	if opts.Compress == "zstd" {
		compression = archive.CompressZstd
	}
	if err := archive.Pack(tmpDir, sidecarBytes, opts.OutputPath, compression); err != nil {
		return err
	}

	return verifyExport(opts.OutputPath, sidecar)
}

// openBaseline returns a BaselineSet from either a transport reference or a
// manifest file on disk. Returns an error when neither is provided.
func openBaseline(ctx context.Context, opts Options) (BaselineSet, error) {
	switch {
	case opts.BaselineRef != nil:
		return NewImageBaseline(ctx, opts.BaselineRef, nil, opts.BaselineRef.StringWithinTransport(), opts.Platform)
	case opts.BaselineManifestPath != "":
		return NewManifestBaseline(opts.BaselineManifestPath, opts.Platform)
	default:
		return nil, fmt.Errorf("no baseline provided: set BaselineRef or BaselineManifestPath")
	}
}

// copyTargetIntoDir materializes the target image into tmpDir, skipping blobs
// whose digests are already known from the baseline.
func copyTargetIntoDir(ctx context.Context, opts Options, tmpDir string, baselineDigests []digest.Digest) error {
	dirRef, err := directory.NewReference(tmpDir)
	if err != nil {
		return fmt.Errorf("new dir reference: %w", err)
	}

	// Wrap the reference so that NewImageDestination returns a KnownBlobsDest.
	// copy.Image calls NewImageDestination on the destRef, so this is the
	// correct injection point.
	destRef := &knownBlobsRef{inner: dirRef, known: baselineDigests}

	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer func() { _ = policyCtx.Destroy() }()

	copyOpts, err := buildCopyOptions(opts.Platform)
	if err != nil {
		return err
	}

	if _, err := copy.Image(ctx, policyCtx, destRef, opts.TargetRef, copyOpts); err != nil {
		return fmt.Errorf("copy target into delta dir: %w", err)
	}
	return nil
}

// knownBlobsRef wraps a types.ImageReference so that NewImageDestination
// returns a KnownBlobsDest pre-seeded with the baseline digests.
type knownBlobsRef struct {
	inner types.ImageReference
	known []digest.Digest
}

func (r *knownBlobsRef) Transport() types.ImageTransport  { return r.inner.Transport() }
func (r *knownBlobsRef) StringWithinTransport() string    { return r.inner.StringWithinTransport() }
func (r *knownBlobsRef) DockerReference() reference.Named { return r.inner.DockerReference() }
func (r *knownBlobsRef) PolicyConfigurationIdentity() string {
	return r.inner.PolicyConfigurationIdentity()
}
func (r *knownBlobsRef) PolicyConfigurationNamespaces() []string {
	return r.inner.PolicyConfigurationNamespaces()
}
func (r *knownBlobsRef) NewImage(ctx context.Context, sys *types.SystemContext) (types.ImageCloser, error) {
	return r.inner.NewImage(ctx, sys)
}
func (r *knownBlobsRef) NewImageSource(ctx context.Context, sys *types.SystemContext) (types.ImageSource, error) {
	return r.inner.NewImageSource(ctx, sys)
}
func (r *knownBlobsRef) NewImageDestination(
	ctx context.Context, sys *types.SystemContext,
) (types.ImageDestination, error) {
	raw, err := r.inner.NewImageDestination(ctx, sys)
	if err != nil {
		return nil, err
	}
	return NewKnownBlobsDest(raw, r.known), nil
}
func (r *knownBlobsRef) DeleteImage(ctx context.Context, sys *types.SystemContext) error {
	return r.inner.DeleteImage(ctx, sys)
}

// Compile-time assertion that knownBlobsRef satisfies types.ImageReference.
var _ types.ImageReference = (*knownBlobsRef)(nil)

// buildCopyOptions returns copy.Options configured for delta export. When
// platform is non-empty, a matching SystemContext is set as SourceCtx so that
// manifest-list sources select the right instance.
func buildCopyOptions(platform string) (*copy.Options, error) {
	opts := &copy.Options{
		PreserveDigests: true,
	}
	if platform != "" {
		sys := &types.SystemContext{}
		if err := applyPlatformToSystemContext(sys, platform); err != nil {
			return nil, err
		}
		opts.SourceCtx = sys
	}
	return opts, nil
}

// buildSidecar reads the manifest written by copy.Image and constructs the
// Sidecar that describes the delta partition.
func buildSidecar(
	dir string, baseline BaselineSet, baselineDigests []digest.Digest, opts Options,
) (diff.Sidecar, error) {
	manifestBytes, mediaType, err := oci.ReadDirManifest(dir)
	if err != nil {
		return diff.Sidecar{}, fmt.Errorf("read exported manifest: %w", err)
	}
	parsed, err := manifest.FromBlob(manifestBytes, mediaType)
	if err != nil {
		return diff.Sidecar{}, fmt.Errorf("parse target manifest: %w", err)
	}

	targetLayers := make([]diff.BlobRef, 0, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		targetLayers = append(targetLayers, diff.BlobRef{
			Digest:    l.Digest,
			Size:      l.Size,
			MediaType: l.MediaType,
		})
	}

	plan := diff.ComputePlan(targetLayers, baselineDigests)

	// Default all shipped entries to full encoding until the IntraLayerPlanner
	// (Task 5) overrides selected entries to patch encoding.
	for i := range plan.ShippedInDelta {
		plan.ShippedInDelta[i].Encoding = diff.EncodingFull
		plan.ShippedInDelta[i].ArchiveSize = plan.ShippedInDelta[i].Size
	}

	platform := opts.Platform
	if platform == "" {
		platform = derivePlatformFromConfig(dir, parsed)
	}

	return diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Tool:        "diffah",
		ToolVersion: opts.ToolVersion,
		CreatedAt:   time.Now().UTC(),
		Platform:    platform,
		Target: diff.ImageRef{
			ManifestDigest: digest.FromBytes(manifestBytes),
			ManifestSize:   int64(len(manifestBytes)),
			MediaType:      mediaType,
		},
		Baseline:             baseline.ManifestRef(),
		RequiredFromBaseline: plan.RequiredFromBaseline,
		ShippedInDelta:       plan.ShippedInDelta,
	}, nil
}

// derivePlatformFromConfig reads the image config blob from the directory
// transport layout and returns "os/arch[/variant]". Returns "" if the config
// is missing or incomplete — upper layers will reject the empty value via
// sidecar validation.
func derivePlatformFromConfig(dir string, parsed manifest.Manifest) string {
	cfgInfo := parsed.ConfigInfo()
	if cfgInfo.Digest == "" {
		return ""
	}
	path := filepath.Join(dir, cfgInfo.Digest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ""
	}
	if cfg.OS == "" || cfg.Architecture == "" {
		return ""
	}
	if cfg.Variant != "" {
		return cfg.OS + "/" + cfg.Architecture + "/" + cfg.Variant
	}
	return cfg.OS + "/" + cfg.Architecture
}

// DryRunStats summarizes an export that was planned but not written.
type DryRunStats struct {
	ShippedCount  int
	ShippedBytes  int64
	RequiredCount int
	RequiredBytes int64
}

// DryRun performs steps 1, 2, and 5 of the export pipeline (see spec §7)
// without calling copy.Image or writing any output files. It returns the
// partition statistics that a real export would produce.
func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	baseline, err := openBaseline(ctx, opts)
	if err != nil {
		return DryRunStats{}, err
	}
	baselineDigests, err := baseline.LayerDigests(ctx)
	if err != nil {
		return DryRunStats{}, fmt.Errorf("load baseline digests: %w", err)
	}

	parsed, err := loadTargetManifest(ctx, opts)
	if err != nil {
		return DryRunStats{}, err
	}

	targetLayers := make([]diff.BlobRef, 0, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		targetLayers = append(targetLayers, diff.BlobRef{
			Digest:    l.Digest,
			Size:      l.Size,
			MediaType: l.MediaType,
		})
	}
	plan := diff.ComputePlan(targetLayers, baselineDigests)

	stats := DryRunStats{
		ShippedCount:  len(plan.ShippedInDelta),
		RequiredCount: len(plan.RequiredFromBaseline),
	}
	for _, b := range plan.ShippedInDelta {
		stats.ShippedBytes += b.Size
	}
	for _, b := range plan.RequiredFromBaseline {
		stats.RequiredBytes += b.Size
	}
	return stats, nil
}

// loadTargetManifest opens the target image source, reads the primary
// manifest (resolving manifest lists via opts.Platform when present), and
// returns the parsed manifest.
func loadTargetManifest(ctx context.Context, opts Options) (manifest.Manifest, error) {
	src, err := opts.TargetRef.NewImageSource(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("open target source: %w", err)
	}
	defer src.Close()

	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("read target manifest: %w", err)
	}

	if manifest.MIMETypeIsMultiImage(mime) {
		chosen, err := selectPlatform(ctx, src, raw, mime, opts.Platform, opts.TargetRef.StringWithinTransport())
		if err != nil {
			return nil, err
		}
		raw, mime = chosen.raw, chosen.mime
	}

	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return nil, fmt.Errorf("parse target manifest: %w", err)
	}
	return parsed, nil
}

// verifyExport re-reads the sidecar from the packed archive and confirms that
// the manifest digest is preserved faithfully.
func verifyExport(archivePath string, want diff.Sidecar) error {
	got, err := archive.ReadSidecar(archivePath)
	if err != nil {
		return fmt.Errorf("verify read sidecar: %w", err)
	}
	back, err := diff.ParseSidecar(got)
	if err != nil {
		return fmt.Errorf("verify parse sidecar: %w", err)
	}
	if back.Target.ManifestDigest != want.Target.ManifestDigest {
		return &diff.ErrDigestMismatch{
			Where: "post-export manifest",
			Want:  string(want.Target.ManifestDigest),
			Got:   string(back.Target.ManifestDigest),
		}
	}
	return nil
}
