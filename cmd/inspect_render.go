package cmd

import (
	"fmt"
	"io"

	"github.com/leosocy/diffah/pkg/importer"
)

// renderLayerTable writes the "Layers (target manifest order):" block.
func renderLayerTable(w io.Writer, d importer.InspectImageDetail) {
	if len(d.Layers) == 0 {
		return
	}
	fmt.Fprintln(w, "  Layers (target manifest order):")
	for _, r := range d.Layers {
		tag := layerTag(r.Kind)
		switch r.Kind {
		case importer.LayerKindFull:
			fmt.Fprintf(w, "    %s  %s %s target / %s archive  (%.2f× — full)\n",
				tag, r.Digest, humanBytes(r.TargetSize), humanBytes(r.ArchiveSize), r.Ratio())
		case importer.LayerKindPatch:
			fmt.Fprintf(w, "    %s  %s %s target / %s archive  (%.2f× — patch from %s)\n",
				tag, r.Digest, humanBytes(r.TargetSize), humanBytes(r.ArchiveSize), r.Ratio(), r.PatchFrom)
		case importer.LayerKindBaselineOnly:
			fmt.Fprintf(w, "    %s  %s %s target /     0 B archive  (— baseline-only)\n",
				tag, r.Digest, humanBytes(r.TargetSize))
		}
	}
}

func layerTag(k importer.LayerRowKind) string {
	switch k {
	case importer.LayerKindFull:
		return "[F]"
	case importer.LayerKindPatch:
		return "[P]"
	case importer.LayerKindBaselineOnly:
		return "[B]"
	}
	return "[?]"
}

// humanBytes prints byte counts in IEC binary units with one-decimal precision
// for non-byte units. The smallest unit (B) prints as a whole number.
func humanBytes(n int64) string {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	switch {
	case n < KiB:
		return fmt.Sprintf("%d B", n)
	case n < MiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(KiB))
	case n < GiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(MiB))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
	}
}

func renderWaste(w io.Writer, d importer.InspectImageDetail) {
	fmt.Fprintln(w, "  Waste:")
	if len(d.Waste) == 0 {
		fmt.Fprintln(w, "    none")
		return
	}
	for _, ws := range d.Waste {
		if ws.Kind == importer.WasteKindPatchOversized {
			fmt.Fprintf(w, "    patch-oversized  %s archive %s ≥ target %s\n",
				ws.Digest, humanBytes(ws.ArchiveSize), humanBytes(ws.TargetSize))
			fmt.Fprintln(w, "                   (patch is bigger than full; force --intra-layer=off for this layer)")
		}
	}
}

const inspectTopNDisplay = 10

func renderTopSavings(w io.Writer, d importer.InspectImageDetail) {
	if len(d.TopSavings) == 0 {
		return
	}
	fmt.Fprintf(w, "  Top savings (%d/%d):\n", len(d.TopSavings), inspectTopNDisplay)
	for i, s := range d.TopSavings {
		fmt.Fprintf(w, "    %d. %s saved %s (%d %%)\n",
			i+1, s.Digest, humanBytes(s.SavedBytes), int(s.SavedRatio*100+0.5))
	}
}

const histogramBarWidth = 12

// histogramDisplayLabels maps internal bucket keys to display strings with
// human-readable separators (en-dash, ≥). Order matches importer.histogramBucketLabels.
var histogramDisplayLabels = []string{
	"< 1 MiB",
	"1–10 MiB",
	"10–100 MiB",
	"100 MiB–1 GiB",
	"≥ 1 GiB",
}

func renderHistogram(w io.Writer, d importer.InspectImageDetail) {
	fmt.Fprintln(w, "  Layer-size histogram (target bytes):")
	maxCount := 0
	for _, c := range d.Histogram.Counts {
		if c > maxCount {
			maxCount = c
		}
	}
	for i, count := range d.Histogram.Counts {
		label := histogramDisplayLabels[i]
		filled := 0
		if maxCount > 0 {
			// Ceiling: a non-empty bucket always shows ≥ 1 filled cell.
			filled = (histogramBarWidth*count + maxCount - 1) / maxCount
			if filled > histogramBarWidth {
				filled = histogramBarWidth
			}
		}
		fmt.Fprintf(w, "    %-14s│%s  %d\n", label, buildBar(filled, histogramBarWidth), count)
	}
}

func buildBar(filled, total int) string {
	out := make([]rune, 0, total)
	for i := 0; i < total; i++ {
		if i < filled {
			out = append(out, '█')
		} else {
			out = append(out, '░')
		}
	}
	return string(out)
}

// imageDetailToJSON returns a flat map of the new per-image JSON keys defined
// in spec §7.6. The caller (inspectJSON in cmd/inspect.go) merges this map
// into the existing per-image entry alongside name/target/baseline.
func imageDetailToJSON(d importer.InspectImageDetail) map[string]any {
	layers := make([]map[string]any, 0, len(d.Layers))
	for _, r := range d.Layers {
		row := map[string]any{
			"digest":       r.Digest.String(),
			"encoding":     string(r.Kind),
			"target_size":  r.TargetSize,
			"archive_size": r.ArchiveSize,
			"saved_bytes":  r.SavedBytes(),
		}
		if r.Kind != importer.LayerKindBaselineOnly {
			row["ratio"] = r.Ratio()
		}
		if r.Kind == importer.LayerKindPatch {
			row["patch_from"] = r.PatchFrom.String()
		} else {
			row["patch_from"] = ""
		}
		layers = append(layers, row)
	}

	waste := make([]map[string]any, 0, len(d.Waste))
	for _, ws := range d.Waste {
		waste = append(waste, map[string]any{
			"kind":         string(ws.Kind),
			"digest":       ws.Digest.String(),
			"archive_size": ws.ArchiveSize,
			"target_size":  ws.TargetSize,
		})
	}

	top := make([]map[string]any, 0, len(d.TopSavings))
	for _, s := range d.TopSavings {
		top = append(top, map[string]any{
			"digest":      s.Digest.String(),
			"saved_bytes": s.SavedBytes,
			"saved_ratio": s.SavedRatio,
		})
	}

	return map[string]any{
		"layer_count":         d.LayerCount,
		"archive_layer_count": d.ArchiveLayerCount,
		"layers":              layers,
		"waste":               waste,
		"top_savings":         top,
		"size_histogram": map[string]any{
			"buckets": d.Histogram.Buckets,
			"counts":  d.Histogram.Counts,
		},
	}
}
