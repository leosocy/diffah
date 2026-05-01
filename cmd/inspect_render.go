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
		switch ws.Kind {
		case importer.WasteKindPatchOversized:
			fmt.Fprintf(w, "    patch-oversized  %s archive %s ≥ target %s\n",
				ws.Digest, humanBytes(ws.ArchiveSize), humanBytes(ws.TargetSize))
			fmt.Fprintln(w, "                   (patch is bigger than full; force --intra-layer=off for this layer)")
		}
	}
}
