// Package importer — per-image admission RSS estimation.
//
// estimatePerImageRSS walks the layers of one ImageEntry's target manifest,
// keeps only those that ship in the bundle (others are baseline-only and
// produce no decoder RSS), and returns the maximum exporter-side RSS
// envelope across them — copy.Image streams layers serially within one
// image, so peak RSS for the image is the worst single layer, not the sum.
//
// checkSingleImageFitsInBudget mirrors exporter's checkSingleLayerFitsInBudget
// for the importer side: if any single image's per-image estimate exceeds the
// admission budget, Import fails immediately with a CategoryUser error before
// opening any worker — same fail-fast contract as Export.
//
// The estimator pulls the windowLog→RSS table through pkg/exporter so the
// import side never under-counts vs what the exporter committed during diff.
package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/exporter"
)

// userError is an importer-package error that carries an errs.Category
// and a remediation hint, satisfying errs.Categorized and errs.Advised
// for the cmd/ exit-code mapper. Mirrors pkg/exporter.userError; the two
// stay duplicated to keep pkg/importer ⊥ pkg/exporter at the package
// boundary (the dependency arrow in this codebase already points
// importer → exporter for the RSS table; importing the type would
// reverse it for shared error machinery).
type userError struct {
	cat  errs.Category
	msg  string
	hint string
}

var (
	_ errs.Categorized = (*userError)(nil)
	_ errs.Advised     = (*userError)(nil)
)

func (e *userError) Error() string           { return e.msg }
func (e *userError) Category() errs.Category { return e.cat }
func (e *userError) NextAction() string      { return e.hint }

// extractLayerDigests reads the per-image target manifest from disk and
// returns its layer digests. Tolerates both OCI v1 and Docker schema-2
// manifests because both store layers under the same JSON shape.
//
// Returns the underlying os/json error wrapped with the manifest digest
// for context; the caller decides whether to treat parse failures as
// per-image failures (preserving sibling progress) or fatal.
func extractLayerDigests(img diff.ImageEntry, blobDir string) ([]digest.Digest, error) {
	mfDigest := img.Target.ManifestDigest
	path := filepath.Join(blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read target manifest %s: %w", mfDigest, err)
	}
	var m struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse target manifest %s: %w", mfDigest, err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("target manifest %s has no layers", mfDigest)
	}
	out := make([]digest.Digest, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out, nil
}

// estimatePerImageRSS returns the conservative peak RSS the apply pipeline
// will hold for one image. It walks every layer in the image's target
// manifest and:
//
//   - skips layers absent from sidecar.Blobs — those are baseline-only
//     reuses (B2 path); they stream from the baseline source without going
//     through DecodeStream, so they don't contribute to the windowLog RSS
//     envelope.
//   - looks up each shipped layer's BlobEntry.Size (uncompressed, used to
//     resolve windowLog), and takes the MAX RSS across all shipped layers.
//
// Why max-not-sum: copy.Image() within a single image processes layers
// sequentially; only one zstd decoder is ever live for one image at a time.
// The pool sums these per-image estimates across concurrent images via the
// admission semaphore — that's where parallelism's RSS cost actually shows up.
//
// userWindowLog is the operator override; 0 selects per-layer-size auto.
func estimatePerImageRSS(
	img diff.ImageEntry,
	blobDir string,
	blobs map[digest.Digest]diff.BlobEntry,
	userWindowLog int,
) (int64, error) {
	layers, err := extractLayerDigests(img, blobDir)
	if err != nil {
		return 0, err
	}
	var maxEst int64
	for _, ld := range layers {
		entry, ok := blobs[ld]
		if !ok {
			// Baseline-only reuse layer; no DecodeStream RSS cost.
			continue
		}
		wl := exporter.ResolveWindowLog(userWindowLog, entry.Size)
		est := exporter.EstimateRSSForWindowLog(wl)
		if est > maxEst {
			maxEst = est
		}
	}
	return maxEst, nil
}

// checkSingleImageFitsInBudget returns a CategoryUser error when any image's
// estimated RSS exceeds memBudget. Called before constructing the admission
// pool so the operator sees the offending image name immediately, before
// any worker spins up.
//
// memBudget == 0 opts out (admission disabled) and returns nil unconditionally.
// A per-image estimate of 0 (e.g. all layers are baseline-only reuses) trivially
// fits any positive budget.
//
// Manifest-parse failures inside this pre-flight are surfaced verbatim — the
// admission step needs every image's estimate to be valid before deciding;
// without it we cannot tell whether some image would overrun later.
func checkSingleImageFitsInBudget(
	images []diff.ImageEntry,
	blobDir string,
	blobs map[digest.Digest]diff.BlobEntry,
	userWindowLog int,
	memBudget int64,
) error {
	if memBudget <= 0 {
		return nil
	}
	for _, img := range images {
		est, err := estimatePerImageRSS(img, blobDir, blobs, userWindowLog)
		if err != nil {
			return err
		}
		if est > memBudget {
			return &userError{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf(
					"image %q requires %d byte(s) of admission budget; --memory-budget is %d",
					img.Name, est, memBudget),
				hint: "increase --memory-budget or set --memory-budget=0 to disable admission",
			}
		}
	}
	return nil
}
