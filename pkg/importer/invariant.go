package importer

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// verifyApplyInvariant re-reads the dest manifest after a successful
// composeImage / copy.Image and proves its layer set + per-layer sizes match
// the sidecar's expectation for this image.
//
// The manifest digest is checked only when the dest media type equals the
// sidecar media type. copy.Image legitimately rewrites manifests when it
// converts between docker-schema2 and OCI v1 (e.g. dest registry only
// accepts OCI), which produces a different digest on the same logical
// image — so a digest mismatch under schema conversion is not a failure.
// Layer set + per-layer size are preserved across that conversion and
// remain authoritative.
//
// Returns nil on match. On any divergence returns *ErrApplyInvariantFailed
// with Missing / Unexpected / Reason populated for actionable diagnostics.
// The sentinel maps to errs.CategoryContent → exit 4 via existing wiring.
func verifyApplyInvariant(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
) error {
	expected, expectedMediaType, err := readSidecarTargetLayers(bundle, img)
	if err != nil {
		return fmt.Errorf("invariant: read sidecar target manifest for image %q: %w",
			img.Name, err)
	}
	actual, actualMediaType, actualManifestDigest, err :=
		readDestManifestLayers(ctx, destRef, sysctx)
	if err != nil {
		return fmt.Errorf("invariant: read dest manifest for image %q: %w",
			img.Name, err)
	}

	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing)+len(unexpected) > 0 {
		return &ErrApplyInvariantFailed{
			ImageName:  img.Name,
			Missing:    missing,
			Unexpected: unexpected,
			Reason:     "layer set mismatch",
		}
	}
	if err := verifyPerLayerSize(expected, actual, bundle.sidecar.Blobs); err != nil {
		return &ErrApplyInvariantFailed{
			ImageName: img.Name,
			Reason:    err.Error(),
		}
	}
	if expectedMediaType == actualMediaType && actualManifestDigest != img.Target.ManifestDigest {
		return &ErrApplyInvariantFailed{
			ImageName: img.Name,
			Reason:    "manifest digest mismatch",
		}
	}
	return nil
}

// layerSetDiff returns the digests present in expected but absent from actual
// (missing), and those present in actual but absent from expected (unexpected).
// Iteration order is map order — callers that need stable output must sort.
func layerSetDiff(expected, actual []LayerRef) (missing, unexpected []digest.Digest) {
	want := make(map[digest.Digest]struct{}, len(expected))
	for _, e := range expected {
		want[e.Digest] = struct{}{}
	}
	have := make(map[digest.Digest]struct{}, len(actual))
	for _, a := range actual {
		have[a.Digest] = struct{}{}
	}
	for d := range want {
		if _, ok := have[d]; !ok {
			missing = append(missing, d)
		}
	}
	for d := range have {
		if _, ok := want[d]; !ok {
			unexpected = append(unexpected, d)
		}
	}
	return missing, unexpected
}

// verifyPerLayerSize confirms each digest shared by expected and actual has
// matching byte sizes. The "want" size is taken from the sidecar BlobEntry
// (authoritative) when available, falling back to the expected manifest's
// own size field otherwise. Layers absent from actual are skipped — those
// are caught by layerSetDiff.
func verifyPerLayerSize(
	expected, actual []LayerRef,
	sidecarBlobs map[digest.Digest]diff.BlobEntry,
) error {
	actualByDigest := make(map[digest.Digest]int64, len(actual))
	for _, a := range actual {
		actualByDigest[a.Digest] = a.Size
	}
	for _, e := range expected {
		gotSize, ok := actualByDigest[e.Digest]
		if !ok {
			continue
		}
		wantSize := e.Size
		if entry, present := sidecarBlobs[e.Digest]; present && entry.Size > 0 {
			wantSize = entry.Size
		}
		if gotSize != wantSize {
			return fmt.Errorf("layer size mismatch: %s want %d got %d",
				e.Digest, wantSize, gotSize)
		}
	}
	return nil
}
