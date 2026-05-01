// Package importer — inspect_data derives a per-image detail summary from a
// parsed sidecar plus the image's target manifest bytes. The output is a
// pure data structure consumed by cmd/inspect's text and JSON renderers.
package importer

import (
	"fmt"
	"sort"

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

// detectWaste flags every patch row whose archive bytes are at least as large
// as its target bytes — i.e., the patch saved nothing or made things worse.
// Order follows the input row order so renderers stay deterministic.
func detectWaste(rows []LayerRow) []WasteEntry {
	var out []WasteEntry
	for _, r := range rows {
		if r.Kind == LayerKindPatch && r.ArchiveSize >= r.TargetSize {
			out = append(out, WasteEntry{
				Kind:        WasteKindPatchOversized,
				Digest:      r.Digest,
				ArchiveSize: r.ArchiveSize,
				TargetSize:  r.TargetSize,
			})
		}
	}
	return out
}

// computeTopSavings returns the top n rows ranked by SavedBytes desc, ties
// broken by digest ascending. Rows with SavedBytes == 0 are excluded.
func computeTopSavings(rows []LayerRow, n int) []TopSaving {
	saved := make([]TopSaving, 0, len(rows))
	for _, r := range rows {
		s := r.SavedBytes()
		if s <= 0 {
			continue
		}
		saved = append(saved, TopSaving{
			Digest:     r.Digest,
			SavedBytes: s,
			SavedRatio: r.SavedRatio(),
		})
	}
	sort.SliceStable(saved, func(i, j int) bool {
		if saved[i].SavedBytes != saved[j].SavedBytes {
			return saved[i].SavedBytes > saved[j].SavedBytes
		}
		return saved[i].Digest < saved[j].Digest
	})
	if len(saved) > n {
		saved = saved[:n]
	}
	return saved
}

const (
	histMiB = 1 << 20
	histGiB = 1 << 30
)

var histogramBucketLabels = []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}

// histogramBucketIndex returns the index in histogramBucketLabels for the
// given target size, using half-open intervals [low, high). The five buckets
// partition [0, ∞).
func histogramBucketIndex(size int64) int {
	switch {
	case size < histMiB:
		return 0
	case size < 10*histMiB:
		return 1
	case size < 100*histMiB:
		return 2
	case size < histGiB:
		return 3
	default:
		return 4
	}
}

// computeHistogram counts target-byte sizes into the five fixed buckets.
func computeHistogram(rows []LayerRow) SizeHistogram {
	counts := make([]int, len(histogramBucketLabels))
	for _, r := range rows {
		counts[histogramBucketIndex(r.TargetSize)]++
	}
	labels := make([]string, len(histogramBucketLabels))
	copy(labels, histogramBucketLabels)
	return SizeHistogram{Buckets: labels, Counts: counts}
}

const inspectTopN = 10

// BuildInspectImageDetail derives the per-image detail block from a parsed
// sidecar, the image's entry within it, and that image's target manifest
// bytes (the canonical bytes whose digest matches img.Target.ManifestDigest).
//
// Pure: no I/O. Returns a wrapped error if the manifest cannot be parsed
// (e.g., manifest list / index, malformed JSON, unsupported media type).
func BuildInspectImageDetail(sc *diff.Sidecar, img diff.ImageEntry, manifestBytes []byte) (InspectImageDetail, error) {
	manifestLayers, _, err := parseManifestLayers(manifestBytes, img.Target.MediaType)
	if err != nil {
		return InspectImageDetail{}, fmt.Errorf("inspect detail for %q: %w", img.Name, err)
	}
	rows := buildLayerRows(manifestLayers, sc.Blobs)

	archiveCount := 0
	for _, r := range rows {
		if r.Kind != LayerKindBaselineOnly {
			archiveCount++
		}
	}

	return InspectImageDetail{
		Name:              img.Name,
		ManifestDigest:    img.Target.ManifestDigest,
		LayerCount:        len(rows),
		ArchiveLayerCount: archiveCount,
		Layers:            rows,
		Waste:             detectWaste(rows),
		TopSavings:        computeTopSavings(rows, inspectTopN),
		Histogram:         computeHistogram(rows),
	}, nil
}
