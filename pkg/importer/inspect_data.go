// Package importer — inspect_data derives a per-image detail summary from a
// parsed sidecar plus the image's target manifest bytes. The output is a
// pure data structure consumed by cmd/inspect's text and JSON renderers.

package importer

import (
	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// LayerRowKind classifies a target-manifest layer by how it was shipped.
type LayerRowKind string

const (
	LayerKindFull         LayerRowKind = "full"          // shipped as Full encoding
	LayerKindPatch        LayerRowKind = "patch"         // shipped as Patch encoding (intra-layer)
	LayerKindBaselineOnly LayerRowKind = "baseline_only" // not shipped; reused from baseline
)

// LayerRow describes one row of the per-image layer table.
type LayerRow struct {
	Digest      digest.Digest
	Kind        LayerRowKind
	TargetSize  int64
	ArchiveSize int64         // 0 for baseline_only
	PatchFrom   digest.Digest // empty unless Kind == Patch
}

// SavedBytes is target_size − archive_size. Baseline-only layers count as full
// savings (target_size).
func (r LayerRow) SavedBytes() int64 {
	if r.Kind == LayerKindBaselineOnly {
		return r.TargetSize
	}
	return r.TargetSize - r.ArchiveSize
}

// SavedRatio is SavedBytes / TargetSize. Returns 0 when TargetSize is 0.
func (r LayerRow) SavedRatio() float64 {
	if r.TargetSize == 0 {
		return 0
	}
	return float64(r.SavedBytes()) / float64(r.TargetSize)
}

// Ratio is archive_size / target_size. Returns 0 when TargetSize is 0 or for
// baseline-only rows. Used for the "(0.06× — patch …)" column.
func (r LayerRow) Ratio() float64 {
	if r.Kind == LayerKindBaselineOnly || r.TargetSize == 0 {
		return 0
	}
	return float64(r.ArchiveSize) / float64(r.TargetSize)
}

// WasteKind tags a single waste-detection category. v1 has only patch_oversized.
type WasteKind string

const WasteKindPatchOversized WasteKind = "patch_oversized"

// WasteEntry is one detected waste row.
type WasteEntry struct {
	Kind        WasteKind
	Digest      digest.Digest
	ArchiveSize int64
	TargetSize  int64
}

// TopSaving is one ranked saving in the top-10 list.
type TopSaving struct {
	Digest     digest.Digest
	SavedBytes int64
	SavedRatio float64 // [0.0, 1.0]
}

// SizeHistogram is the five-bucket log-scale distribution of LayerRow target sizes.
// Buckets and Counts have identical length and bucket order.
type SizeHistogram struct {
	Buckets []string
	Counts  []int
}

// InspectImageDetail is the full per-image enrichment produced by
// BuildInspectImageDetail.
type InspectImageDetail struct {
	Name              string
	ManifestDigest    digest.Digest
	LayerCount        int // total layers in target manifest
	ArchiveLayerCount int // layers shipped (Full + Patch); LayerCount − len(BaselineOnly)
	Layers            []LayerRow
	Waste             []WasteEntry
	TopSavings        []TopSaving
	Histogram         SizeHistogram
}

// buildLayerRows produces one LayerRow per target-manifest layer, in target
// manifest order. Full / Patch are looked up in blobs; absent digests produce
// a baseline-only row.
func buildLayerRows(manifestLayers []LayerRef, blobs map[digest.Digest]diff.BlobEntry) []LayerRow {
	rows := make([]LayerRow, 0, len(manifestLayers))
	for _, l := range manifestLayers {
		b, ok := blobs[l.Digest]
		if !ok {
			rows = append(rows, LayerRow{
				Digest:     l.Digest,
				Kind:       LayerKindBaselineOnly,
				TargetSize: l.Size,
			})
			continue
		}
		row := LayerRow{
			Digest:      l.Digest,
			TargetSize:  l.Size,
			ArchiveSize: b.ArchiveSize,
		}
		switch b.Encoding {
		case diff.EncodingFull:
			row.Kind = LayerKindFull
		case diff.EncodingPatch:
			row.Kind = LayerKindPatch
			row.PatchFrom = b.PatchFromDigest
		default:
			// Validated upstream by sidecar.validate(); fall through to Full as a
			// defensive default so renderers don't crash on a malformed but parsed
			// sidecar.
			row.Kind = LayerKindFull
		}
		rows = append(rows, row)
	}
	return rows
}
