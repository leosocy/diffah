// Package importer — per-image admission RSS estimation.
//
// estimatePerImageRSS walks the layers of one ImageEntry's target manifest
// and returns the maximum apply-time RSS contribution across shipped and
// baseline-only layers. copy.Image streams layers serially within one image,
// so peak RSS for the image is the worst single layer, not the sum.
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

// estimatePerImageRSS returns the conservative peak RSS the apply pipeline
// will hold for one image. It walks every layer in the image's target
// manifest and:
//
//   - shipped layers contribute EstimateRSSForWindowLog(ResolveWindowLog(...))
//     using the encoder-committed window envelope.
//   - baseline-only layers contribute LayerRef.Size, because the spooled file
//     is streamed into the destination and the page-cache envelope still counts
//     against the operator's --memory-budget mental model.
//
// Why max-not-sum: copy.Image() within a single image processes layers
// sequentially (MaxParallelDownloads: 1 is enforced in composeImage).
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
	layers, err := readManifestLayerRefs(blobDir, img.Target.ManifestDigest)
	if err != nil {
		return 0, fmt.Errorf("read target manifest %s: %w", img.Target.ManifestDigest, err)
	}
	var maxEst int64
	for _, ld := range layers {
		var est int64
		if entry, ok := blobs[ld.Digest]; ok {
			wl := exporter.ResolveWindowLog(userWindowLog, entry.Size)
			est = exporter.EstimateRSSForWindowLog(wl)
		} else {
			est = ld.Size
		}
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
			return &errs.UserError{
				Cat: errs.CategoryUser,
				Msg: fmt.Sprintf(
					"image %q requires %d byte(s) of admission budget; --memory-budget is %d",
					img.Name, est, memBudget),
				Hint: "increase --memory-budget or set --memory-budget=0 to disable admission",
			}
		}
	}
	return nil
}

func readManifestLayerRefs(blobDir string, mfDigest digest.Digest) ([]LayerRef, error) {
	path := filepath.Join(blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
			Size   int64         `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	out := make([]LayerRef, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, LayerRef{Digest: l.Digest, Size: l.Size})
	}
	return out, nil
}
