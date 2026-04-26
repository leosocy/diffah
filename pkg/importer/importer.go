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

	preflightResults, anyPreflightFail, perr := RunPreflight(ctx, bundle, resolved, opts.SystemContext, rep)
	if perr != nil {
		// Schema-error: bundle-internal manifest failed to parse. Fatal in
		// every mode — never produce a partial success when the delta
		// itself is malformed.
		return perr
	}
	if opts.Strict && anyPreflightFail {
		// Strict mode performs a scan-all-then-abort: every image is
		// classified before we exit, so the operator sees the complete
		// failure picture rather than discovering issues one --strict
		// run at a time.
		return abortWithPreflightSummary(preflightResults)
	}

	applyList, skippedByPreflight := splitPreflightResults(preflightResults)
	cache := newBaselineBlobCache()
	report := importEachImage(ctx, bundle, resolvedByName, outputs, opts, cache, applyList)
	report.Total = len(bundle.sidecar.Images)
	mergePreflightSkips(&report, skippedByPreflight)
	finalizeImportReport(ctx, rep, report, len(skippedByPreflight))

	// Partial-mode contract: at least one image must succeed for an
	// exit-0 outcome. Returning the first non-OK image's error keeps
	// classification (CategoryContent for B1/B2/invariant) intact for
	// cmd.Execute's exit-code mapping.
	if report.Successful() == 0 && report.Total > 0 {
		return firstNonOKError(report)
	}
	return nil
}

// mergePreflightSkips appends one ApplyImageSkippedPreflight entry per
// preflight-rejected image into the apply-time report, mapping the
// preflight status back to its categorized error sentinel.
func mergePreflightSkips(report *ApplyReport, skipped map[string]PreflightResult) {
	for name, r := range skipped {
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: name,
			Status:    ApplyImageSkippedPreflight,
			Err:       preflightResultToErr(r),
		})
	}
}

// finalizeImportReport renders the final summary to stderr, emits the
// completion log line, and signals the reporter's terminal "done" phase.
// Side-effect-only — does not consult the report for return values.
func finalizeImportReport(ctx context.Context, rep progress.Reporter, report ApplyReport, skippedCount int) {
	renderSummary(os.Stderr, report)
	log().InfoContext(ctx, "import complete",
		"imported", report.Successful(), "total", report.Total,
		"skipped_preflight", skippedCount)
	rep.Phase("done")
}

// splitPreflightResults partitions per-image PreflightResult outcomes into
// the apply-list (PreflightOK) and the skipped-by-preflight map (everything
// else). Pulling this out of Import keeps the function below the
// complexity threshold without introducing a struct.
func splitPreflightResults(results []PreflightResult) ([]string, map[string]PreflightResult) {
	applyList := make([]string, 0, len(results))
	skipped := make(map[string]PreflightResult)
	for _, r := range results {
		if r.Status == PreflightOK {
			applyList = append(applyList, r.ImageName)
		} else {
			skipped[r.ImageName] = r
		}
	}
	return applyList, skipped
}

// importEachImage composes and verifies the images named in applyList,
// recording per-image outcomes in an ApplyReport. In strict mode the loop
// stops at the first compose/invariant failure (preserving the "abort on
// first error" feel for callers that opted in via --strict). In partial mode
// the loop continues so the final summary captures every outcome. Total is
// not set here — Import populates it from the full image list after merging
// preflight skips.
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
	cache *baselineBlobCache,
	applyList []string,
) ApplyReport {
	report := ApplyReport{}
	for _, name := range applyList {
		result := applyOneImage(ctx, name, bundle, resolvedByName, outputs, opts, cache)
		report.Results = append(report.Results, result)
		if opts.Strict && result.Status != ApplyImageOK {
			return report
		}
	}
	return report
}

// applyOneImage runs the full per-image pipeline (resolve output, compose,
// invariant verify) and returns the typed result. Splitting this out of
// importEachImage keeps the loop body short and lets each failure path
// share one append-and-record idiom.
func applyOneImage(
	ctx context.Context,
	name string,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
	cache *baselineBlobCache,
) ApplyImageResult {
	img, ok := findImageByName(bundle.sidecar.Images, name)
	if !ok {
		return ApplyImageResult{
			ImageName: name, Status: ApplyImageFailedCompose,
			Err: fmt.Errorf("image %q not found in sidecar", name),
		}
	}
	rb, ok := resolvedByName[name]
	if !ok {
		// Pre-flight already enforced baseline presence for OK images;
		// reaching here with no resolved baseline is a programming error.
		return ApplyImageResult{
			ImageName: name, Status: ApplyImageFailedCompose,
			Err: fmt.Errorf("no resolved baseline for image %q", name),
		}
	}
	destRef, err := prepareDestRef(name, outputs)
	if err != nil {
		return ApplyImageResult{
			ImageName: name, Status: ApplyImageFailedCompose, Err: err,
		}
	}
	if err := composeImage(ctx, img, bundle, rb, destRef,
		opts.SystemContext, opts.AllowConvert, opts.reporter(), cache); err != nil {
		return ApplyImageResult{
			ImageName: name, Status: ApplyImageFailedCompose, Err: err,
		}
	}
	if err := verifyApplyInvariant(ctx, img, bundle, destRef, opts.SystemContext); err != nil {
		return ApplyImageResult{
			ImageName: name, Status: ApplyImageFailedInvariant, Err: err,
		}
	}
	return ApplyImageResult{ImageName: name, Status: ApplyImageOK}
}

// prepareDestRef looks up the output spec for name, ensures the parent
// directory exists for file-based transports, and parses the reference.
func prepareDestRef(name string, outputs map[string]string) (types.ImageReference, error) {
	rawOut, ok := outputs[name]
	if !ok {
		return nil, fmt.Errorf("no output reference in OUTPUT-SPEC for image %q", name)
	}
	if err := ensureOutputParent(rawOut); err != nil {
		return nil, fmt.Errorf("prepare output for image %q: %w", name, err)
	}
	destRef, err := imageio.ParseReference(rawOut)
	if err != nil {
		return nil, fmt.Errorf("parse output reference for image %q: %w", name, err)
	}
	return destRef, nil
}

func findImageByName(imgs []diff.ImageEntry, name string) (diff.ImageEntry, bool) {
	for _, img := range imgs {
		if img.Name == name {
			return img, true
		}
	}
	return diff.ImageEntry{}, false
}

// preflightResultToErr maps a non-OK PreflightResult back into the
// project's existing apply-time error sentinels so categorization
// (CategoryContent → exit 4) and NextAction hints reach cmd.Execute
// uniformly whether the failure was caught at preflight or apply.
func preflightResultToErr(r PreflightResult) error {
	switch r.Status {
	case PreflightMissingPatchSource:
		return &ErrMissingPatchSource{
			ImageName:       r.ImageName,
			PatchFromDigest: firstOrEmptyDigest(r.MissingPatchSources),
		}
	case PreflightMissingReuseLayer:
		return &ErrMissingBaselineReuseLayer{
			ImageName:   r.ImageName,
			LayerDigest: firstOrEmptyDigest(r.MissingReuseLayers),
		}
	case PreflightError, PreflightSchemaError:
		return r.Err
	}
	return nil
}

func firstOrEmptyDigest(ds []digest.Digest) digest.Digest {
	if len(ds) == 0 {
		return ""
	}
	return ds[0]
}

// abortWithPreflightSummary renders the strict-mode summary and returns
// the first non-OK image's classified sentinel so cmd.Execute exits 4.
// The full failure picture is on stderr already; the returned error is
// only the error code carrier.
func abortWithPreflightSummary(results []PreflightResult) error {
	report := ApplyReport{Total: len(results)}
	for _, r := range results {
		status := ApplyImageOK
		if r.Status != PreflightOK {
			status = ApplyImageSkippedPreflight
		}
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: r.ImageName, Status: status, Err: preflightResultToErr(r),
		})
	}
	renderSummary(os.Stderr, report)
	for _, r := range results {
		if r.Status != PreflightOK {
			return preflightResultToErr(r)
		}
	}
	// Defensive: caller only invokes us when anyPreflightFail is true.
	return fmt.Errorf("preflight rejected images (--strict)")
}

func firstNonOKError(report ApplyReport) error {
	for _, r := range report.Results {
		if r.Status != ApplyImageOK && r.Err != nil {
			return r.Err
		}
	}
	return fmt.Errorf("0 of %d images succeeded", report.Total)
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
