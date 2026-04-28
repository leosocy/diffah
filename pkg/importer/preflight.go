package importer

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

// PreflightStatus classifies the outcome of scanning a single image's
// baseline against what its delta requires. PreflightOK means every
// baseline-only-reuse layer and every patch source layer the delta
// references is present in the baseline manifest. The other statuses
// are mutually exclusive failure categories suitable for a final
// summary report.
type PreflightStatus int

const (
	// PreflightOK indicates the baseline contains every required digest
	// and the delta can be applied without further fetches beyond the
	// archive's own blobs.
	PreflightOK PreflightStatus = iota
	// PreflightMissingPatchSource (B1) — the delta ships at least one
	// patch blob whose patch_from_digest is absent from the baseline.
	// Apply cannot reconstruct the patched layer.
	PreflightMissingPatchSource
	// PreflightMissingReuseLayer (B2) — the target manifest references
	// a layer the delta did not ship and the baseline does not contain.
	// Apply cannot satisfy that layer from any source.
	PreflightMissingReuseLayer
	// PreflightError covers transport / I/O failures encountered during
	// the scan itself (baseline manifest fetch failure, etc.). Not a
	// content classification — partial mode treats it the same as a
	// missing-baseline skip; strict mode aborts.
	PreflightError
	// PreflightSchemaError is a fatal classification: the bundle's own
	// target manifest could not be parsed. Returned to the caller as a
	// hard error regardless of strict/partial mode.
	PreflightSchemaError
)

// PreflightResult is the per-image outcome of a preflight scan. The
// MissingPatchSources and MissingReuseLayers slices are populated
// independently so a single image may report both classes of missing
// digests; Status records the dominant category for routing.
type PreflightResult struct {
	ImageName           string
	Status              PreflightStatus
	MissingPatchSources []digest.Digest
	MissingReuseLayers  []digest.Digest
	Err                 error
}

// computeRequiredBaselineDigests returns the baseline-only reuse layer
// set (B2 candidates) and the patch-source set (B1 candidates) inferred
// from the target manifest plus sidecar. Layers shipped with
// EncodingFull contribute nothing — they are satisfied by archive
// blobs. Layers shipped with EncodingPatch contribute their
// PatchFromDigest. Layers absent from sidecar.Blobs must come from the
// baseline directly and become reuse candidates.
func computeRequiredBaselineDigests(
	bundle *extractedBundle, img diff.ImageEntry,
) (reuse, patchSrcs []digest.Digest, err error) {
	targetLayers, _, err := readSidecarTargetLayers(bundle, img)
	if err != nil {
		return nil, nil, err
	}
	shipped := bundle.sidecar.Blobs

	reuseSet := make(map[digest.Digest]struct{})
	patchSet := make(map[digest.Digest]struct{})
	for _, layer := range targetLayers {
		entry, isShipped := shipped[layer.Digest]
		if !isShipped {
			reuseSet[layer.Digest] = struct{}{}
			continue
		}
		if entry.Encoding == diff.EncodingPatch && entry.PatchFromDigest != "" {
			patchSet[entry.PatchFromDigest] = struct{}{}
		}
	}
	return digestSetSlice(reuseSet), digestSetSlice(patchSet), nil
}

func digestSetSlice(s map[digest.Digest]struct{}) []digest.Digest {
	out := make([]digest.Digest, 0, len(s))
	for d := range s {
		out = append(out, d)
	}
	return out
}

// scanOneImage performs the per-image preflight scan against the baseline
// manifest bytes captured at resolve time. On parse failure it returns
// Status=PreflightError with Err populated; the caller decides whether that
// is fatal (strict) or skip-and-continue (partial). PreflightSchemaError is
// reserved for the bundle's own target manifest being unparseable, which is
// always fatal.
func scanOneImage(
	ctx context.Context,
	bundle *extractedBundle,
	img diff.ImageEntry,
	rb resolvedBaseline,
) PreflightResult {
	if err := ctx.Err(); err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	reuse, patchSrcs, err := computeRequiredBaselineDigests(bundle, img)
	if err != nil {
		return PreflightResult{
			ImageName: img.Name, Status: PreflightSchemaError, Err: err,
		}
	}
	baselineLayers, cfgDigest, _, err := parseManifest(rb.ManifestBytes, rb.ManifestMime)
	if err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	baselineSet := make(map[digest.Digest]struct{}, len(baselineLayers)+1)
	for _, l := range baselineLayers {
		baselineSet[l.Digest] = struct{}{}
	}
	// Config is reachable from the baseline; including it prevents a false
	// B1 when a patch source happens to be the config blob.
	if cfgDigest != "" {
		baselineSet[cfgDigest] = struct{}{}
	}

	missingPatchSrcs := digestsNotIn(patchSrcs, baselineSet)
	missingReuse := digestsNotIn(reuse, baselineSet)

	if len(missingPatchSrcs) == 0 && len(missingReuse) == 0 {
		return PreflightResult{ImageName: img.Name, Status: PreflightOK}
	}
	// B1 dominates B2 in the categorical Status field; both digest slices
	// are populated independently so callers can render the full picture.
	status := PreflightMissingReuseLayer
	if len(missingPatchSrcs) > 0 {
		status = PreflightMissingPatchSource
	}
	return PreflightResult{
		ImageName:           img.Name,
		Status:              status,
		MissingPatchSources: missingPatchSrcs,
		MissingReuseLayers:  missingReuse,
	}
}

func digestsNotIn(want []digest.Digest, have map[digest.Digest]struct{}) []digest.Digest {
	out := []digest.Digest{}
	for _, d := range want {
		if _, ok := have[d]; !ok {
			out = append(out, d)
		}
	}
	return out
}

// RunPreflight scans every image in the bundle against its resolved baseline
// and returns per-image results plus an aggregate failure flag. A
// PreflightSchemaError on any image is fatal and surfaces via the err return
// value (regardless of strict/partial mode), because an unparseable target
// manifest indicates a corrupt or unsupported delta. Other non-OK statuses
// are recorded in the result slice for the caller (Import) to route on.
func RunPreflight(
	ctx context.Context,
	bundle *extractedBundle,
	resolved []resolvedBaseline,
	reporter progress.Reporter,
) ([]PreflightResult, bool, error) {
	resolvedByName := make(map[string]resolvedBaseline, len(resolved))
	for _, r := range resolved {
		resolvedByName[r.Name] = r
	}
	if reporter != nil {
		reporter.Phase("preflight")
	}

	results := make([]PreflightResult, 0, len(bundle.sidecar.Images))
	anyFail := false
	for _, img := range bundle.sidecar.Images {
		if err := ctx.Err(); err != nil {
			return results, anyFail, err
		}
		rb, ok := resolvedByName[img.Name]
		if !ok {
			results = append(results, PreflightResult{
				ImageName: img.Name, Status: PreflightError,
				Err: fmt.Errorf("baseline not resolved for image %q", img.Name),
			})
			anyFail = true
			continue
		}
		r := scanOneImage(ctx, bundle, img, rb)
		if r.Status == PreflightSchemaError {
			return results, true, r.Err
		}
		if r.Status != PreflightOK {
			anyFail = true
		}
		results = append(results, r)
		log().InfoContext(ctx, "preflight",
			"image", r.ImageName, "status", r.Status.String(),
			"missing_patch_sources", len(r.MissingPatchSources),
			"missing_reuse_layers", len(r.MissingReuseLayers))
	}
	return results, anyFail, nil
}

// String returns a human-readable label for the status. Used by slog
// structured logging and the final summary renderer.
func (s PreflightStatus) String() string {
	switch s {
	case PreflightOK:
		return "ok"
	case PreflightMissingPatchSource:
		return "missing-patch-source"
	case PreflightMissingReuseLayer:
		return "missing-reuse-layer"
	case PreflightError:
		return "error"
	case PreflightSchemaError:
		return "schema-error"
	}
	return "unknown"
}
