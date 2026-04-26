package importer

import (
	"github.com/opencontainers/go-digest"

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
