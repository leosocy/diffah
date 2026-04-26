package importer

import (
	"context"
	"encoding/json"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
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

// scanOneImage performs the per-image preflight scan against an already-opened
// baseline ImageSource (typically resolvedBaseline.Src). On baseline manifest
// fetch or parse failure it returns Status=PreflightError with Err populated;
// the caller decides whether that is fatal (strict) or skip-and-continue
// (partial). PreflightSchemaError is reserved for the bundle's own target
// manifest being unparseable, which is always fatal.
func scanOneImage(
	ctx context.Context,
	bundle *extractedBundle,
	img diff.ImageEntry,
	baseline types.ImageSource,
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
	rawMf, mfMime, err := baseline.GetManifest(ctx, nil)
	if err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	baselineLayers, _, err := parseManifestLayers(rawMf, mfMime)
	if err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	baselineSet := make(map[digest.Digest]struct{}, len(baselineLayers)+1)
	for _, l := range baselineLayers {
		baselineSet[l.Digest] = struct{}{}
	}
	// Config digest is also reachable from the baseline and may be reused
	// directly by patch sources / config fetches; include it in the
	// reachability set so a baseline that merely lists its config doesn't
	// trigger a false B1.
	if cfg, _ := configDigestOf(rawMf); cfg != "" {
		baselineSet[cfg] = struct{}{}
	}

	missingPatchSrcs := digestsNotIn(patchSrcs, baselineSet)
	missingReuse := digestsNotIn(reuse, baselineSet)

	res := PreflightResult{
		ImageName:           img.Name,
		MissingPatchSources: missingPatchSrcs,
		MissingReuseLayers:  missingReuse,
	}
	switch {
	case len(missingPatchSrcs) == 0 && len(missingReuse) == 0:
		res.Status = PreflightOK
	case len(missingPatchSrcs) > 0:
		// B1 dominates B2 in the categorical Status field; both digest
		// slices are populated independently so callers can render the
		// full picture.
		res.Status = PreflightMissingPatchSource
	default:
		res.Status = PreflightMissingReuseLayer
	}
	return res
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

// configDigestOf parses raw manifest bytes (Docker schema 2 or OCI v1) and
// returns the config descriptor's digest. The two formats share the same
// "config":{"digest":...} JSON shape, so a minimal projection works for both.
func configDigestOf(raw []byte) (digest.Digest, error) {
	type descriptor struct {
		Digest digest.Digest `json:"digest"`
	}
	type minimal struct {
		Config descriptor `json:"config"`
	}
	var m minimal
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	return m.Config.Digest, nil
}
