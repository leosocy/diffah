package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
	"github.com/leosocy/diffah/pkg/signer"
)

type Options struct {
	DeltaPath        string
	Baselines        map[string]string // transport-prefixed refs
	Outputs          map[string]string // transport-prefixed dest refs
	Strict           bool
	SystemContext    *types.SystemContext
	AllowConvert     bool
	RetryTimes       int           // default 0 = no retry; applied to baseline manifest+source open
	RetryDelay       time.Duration // zero means exponential backoff (capped by withRetry)
	ProgressReporter progress.Reporter
	// Deprecated: use ProgressReporter. Will be removed in v0.4.
	Progress io.Writer
	Probe    func(context.Context) (bool, string)
	// VerifyPubKeyPath enables signature verification when non-empty.
	// The file at this path must hold a PEM-encoded ECDSA-P256 PKIX
	// public key. When empty, Import does not read any .sig / .cert /
	// .rekor.json sidecar files — signed archives are processed
	// byte-identically to unsigned ones.
	VerifyPubKeyPath string
	// VerifyRekorURL is the Rekor transparency-log URL against which
	// an accompanying .rekor.json inclusion proof is checked. Only
	// consulted when .rekor.json is present; missing bundle does not
	// fail the verify.
	VerifyRekorURL string
}

func (o *Options) reporter() progress.Reporter {
	if o.ProgressReporter != nil {
		return o.ProgressReporter
	}
	return progress.FromWriter(o.Progress)
}

func (o *Options) probeOrDefault() func(context.Context) (bool, string) {
	if o.Probe != nil {
		return o.Probe
	}
	return zstdpatch.Available
}

func Import(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return err
	}
	defer bundle.cleanup()

	if opts.VerifyPubKeyPath != "" {
		if err := verifySignature(ctx, opts.DeltaPath, bundle.sidecarRawBytes, opts); err != nil {
			return err
		}
	}

	if bundle.sidecar.RequiresZstd() {
		ok, reason := opts.probeOrDefault()(ctx)
		if !ok {
			return fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
		}
	}

	if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
		return err
	}
	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.SystemContext,
		opts.RetryTimes, opts.RetryDelay, opts.Strict)
	if err != nil {
		return err
	}
	defer closeResolvedBaselines(resolved)

	outputs := expandDefaultOutput(bundle.sidecar, opts.Outputs)

	rep := opts.reporter()
	rep.Phase("extracting")

	resolvedByName := make(map[string]resolvedBaseline, len(resolved))
	for _, r := range resolved {
		resolvedByName[r.Name] = r
	}

	imported, skipped, err := importEachImage(ctx, bundle, resolvedByName, outputs, opts)
	if err != nil {
		return err
	}
	log().InfoContext(ctx, "import complete",
		"imported", imported, "total", len(bundle.sidecar.Images), "skipped", skipped)
	rep.Phase("done")
	return nil
}

// importEachImage composes and writes every image in the bundle for which a
// baseline was resolved. Images without a resolved baseline are recorded in
// the skipped list and not composed; --strict is enforced earlier by
// resolveBaselines, so reaching here with an unresolved image implies the
// caller opted into non-strict mode.
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
) (int, []string, error) {
	imported := 0
	skipped := make([]string, 0)
	for _, img := range bundle.sidecar.Images {
		rb, ok := resolvedByName[img.Name]
		if !ok {
			log().WarnContext(ctx, "skipped image: no baseline provided", "image", img.Name)
			skipped = append(skipped, img.Name)
			continue
		}
		rawOut, ok := outputs[img.Name]
		if !ok {
			return 0, nil, fmt.Errorf("no output reference in OUTPUT-SPEC for image %q", img.Name)
		}
		if err := ensureOutputParent(rawOut); err != nil {
			return 0, nil, fmt.Errorf("prepare output for image %q: %w", img.Name, err)
		}
		destRef, err := imageio.ParseReference(rawOut)
		if err != nil {
			return 0, nil, fmt.Errorf("parse output reference for image %q: %w", img.Name, err)
		}
		if err := composeImage(ctx, img, bundle, rb, destRef,
			opts.SystemContext, opts.AllowConvert, opts.reporter()); err != nil {
			return 0, nil, err
		}
		imported++
	}
	return imported, skipped, nil
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

	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.SystemContext,
		opts.RetryTimes, opts.RetryDelay, opts.Strict)
	if err != nil {
		return DryRunReport{}, err
	}
	defer closeResolvedBaselines(resolved)

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

// verifySignature loads the sidecar files adjacent to the archive and,
// if the archive is signed, asserts the signature matches the public
// key at opts.VerifyPubKeyPath. Called only when VerifyPubKeyPath is
// set. Returns signer.ErrArchiveUnsigned (CategoryContent) when
// --verify was supplied but no .sig file exists.
//
// The canonical digest is computed from sidecarBytes — the on-disk
// diffah.json tar entry bytes — not a re-serialized sidecar struct,
// so the digest matches what the exporter signed byte-for-byte even
// if the parsed struct drops unknown fields.
func verifySignature(ctx context.Context, deltaPath string, sidecarBytes []byte, opts Options) error {
	sig, err := signer.LoadSidecars(deltaPath)
	if err != nil {
		return err
	}
	if sig == nil {
		return signer.ErrArchiveUnsigned
	}
	payload, err := signer.PayloadDigestFromSidecar(sidecarBytes)
	if err != nil {
		return err
	}
	opts.reporter().Phase("verifying")
	return signer.Verify(ctx, opts.VerifyPubKeyPath, payload[:], sig, opts.VerifyRekorURL)
}

// ensureOutputParent creates the parent directory for file-based output
// transports (oci-archive:, docker-archive:, dir:). Remote transports
// (docker://, etc.) are skipped. This is required because transport
// ParseReference implementations resolve the path and require the parent
// to exist.
func ensureOutputParent(rawOut string) error {
	transport, path, ok := strings.Cut(rawOut, ":")
	if !ok {
		return nil
	}
	switch transport {
	case FormatOCIArchive, FormatDockerArchive:
		parent := filepath.Dir(path)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("mkdir parent for %s output %s: %w", transport, path, err)
		}
	case FormatDir, "oci":
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("mkdir %s output %s: %w", transport, path, err)
		}
	}
	return nil
}
