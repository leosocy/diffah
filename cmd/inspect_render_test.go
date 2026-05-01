package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/importer"
)

func dig(s string) digest.Digest { return digest.Digest("sha256:" + s) }

func TestRenderLayerTable_FullPatchBaseline(t *testing.T) {
	detail := importer.InspectImageDetail{
		Layers: []importer.LayerRow{
			{Digest: dig(strings.Repeat("a", 64)), Kind: importer.LayerKindFull, TargetSize: 13_000_000, ArchiveSize: 13_000_000},
			{Digest: dig(strings.Repeat("b", 64)), Kind: importer.LayerKindPatch, TargetSize: 8_000_000, ArchiveSize: 500_000, PatchFrom: dig(strings.Repeat("z", 64))},
			{Digest: dig(strings.Repeat("c", 64)), Kind: importer.LayerKindBaselineOnly, TargetSize: 5_000_000},
		},
	}

	var buf bytes.Buffer
	renderLayerTable(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Layers (target manifest order):")
	require.Contains(t, out, "[F]")
	require.Contains(t, out, "[P]")
	require.Contains(t, out, "[B]")
	require.Contains(t, out, "1.00× — full")
	require.Contains(t, out, "0.06× — patch from sha256:")
	require.Contains(t, out, "— baseline-only")
}

func TestRenderWaste_PatchOversizedShowsHint(t *testing.T) {
	detail := importer.InspectImageDetail{
		Waste: []importer.WasteEntry{
			{Kind: importer.WasteKindPatchOversized, Digest: dig(strings.Repeat("y", 64)), ArchiveSize: 12_000_000, TargetSize: 8_000_000},
		},
	}
	var buf bytes.Buffer
	renderWaste(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Waste:")
	require.Contains(t, out, "patch-oversized")
	require.Contains(t, out, "patch is bigger than full")
}

func TestRenderWaste_NoneWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderWaste(&buf, importer.InspectImageDetail{})
	require.Contains(t, buf.String(), "Waste:")
	require.Contains(t, buf.String(), "    none")
}

func TestRenderTopSavings_PrintsRankedRows(t *testing.T) {
	detail := importer.InspectImageDetail{
		TopSavings: []importer.TopSaving{
			{Digest: dig(strings.Repeat("x", 64)), SavedBytes: 7_500_000, SavedRatio: 0.94},
			{Digest: dig(strings.Repeat("y", 64)), SavedBytes: 1_500_000, SavedRatio: 0.50},
		},
	}
	var buf bytes.Buffer
	renderTopSavings(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Top savings (2/10):")
	require.Contains(t, out, "1. sha256:")
	require.Contains(t, out, "(94 %)")
	require.Contains(t, out, "2. sha256:")
}

func TestRenderTopSavings_OmittedWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderTopSavings(&buf, importer.InspectImageDetail{})
	require.Empty(t, buf.String())
}

func TestRenderHistogram_FilledAndEmptyBars(t *testing.T) {
	detail := importer.InspectImageDetail{
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{2, 6, 3, 0, 0},
		},
	}
	var buf bytes.Buffer
	renderHistogram(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Layer-size histogram (target bytes):")
	require.Contains(t, out, "< 1 MiB")
	require.Contains(t, out, "1–10 MiB")
	require.Contains(t, out, "10–100 MiB")
	require.Contains(t, out, "100 MiB–1 GiB")
	require.Contains(t, out, "≥ 1 GiB")
	require.Contains(t, out, "█")
	require.Contains(t, out, "░")
}

func TestRenderHistogram_AllZeroPrintsAllEmptyBars(t *testing.T) {
	detail := importer.InspectImageDetail{
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{0, 0, 0, 0, 0},
		},
	}
	var buf bytes.Buffer
	renderHistogram(&buf, detail)
	require.Contains(t, buf.String(), "░░░░░░░░░░░░")
}

func TestImageDetailToJSON_Shape(t *testing.T) {
	detail := importer.InspectImageDetail{
		Name:              "svc",
		ManifestDigest:    dig(strings.Repeat("a", 64)),
		LayerCount:        3,
		ArchiveLayerCount: 2,
		Layers: []importer.LayerRow{
			{Digest: dig(strings.Repeat("a", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			{Digest: dig(strings.Repeat("b", 64)), Kind: importer.LayerKindPatch, TargetSize: 800, ArchiveSize: 50, PatchFrom: dig(strings.Repeat("z", 64))},
			{Digest: dig(strings.Repeat("c", 64)), Kind: importer.LayerKindBaselineOnly, TargetSize: 500},
		},
		Waste: []importer.WasteEntry{
			{Kind: importer.WasteKindPatchOversized, Digest: dig(strings.Repeat("y", 64)), ArchiveSize: 1200, TargetSize: 800},
		},
		TopSavings: []importer.TopSaving{
			{Digest: dig(strings.Repeat("c", 64)), SavedBytes: 500, SavedRatio: 1.0},
		},
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{3, 0, 0, 0, 0},
		},
	}

	got := imageDetailToJSON(detail)

	layers := got["layers"].([]map[string]any)
	require.Len(t, layers, 3)
	require.Equal(t, "full", layers[0]["encoding"])
	require.EqualValues(t, 1000, layers[0]["target_size"])
	require.InDelta(t, 1.0, layers[0]["ratio"], 0.001)
	require.EqualValues(t, 0, layers[0]["saved_bytes"])

	require.Equal(t, "patch", layers[1]["encoding"])
	require.Equal(t, "sha256:"+strings.Repeat("z", 64), layers[1]["patch_from"])

	require.Equal(t, "baseline_only", layers[2]["encoding"])
	require.NotContains(t, layers[2], "ratio")

	waste := got["waste"].([]map[string]any)
	require.Len(t, waste, 1)
	require.Equal(t, "patch_oversized", waste[0]["kind"])

	top := got["top_savings"].([]map[string]any)
	require.Len(t, top, 1)
	require.EqualValues(t, 500, top[0]["saved_bytes"])

	hist := got["size_histogram"].(map[string]any)
	require.Equal(t, []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}, hist["buckets"])
	require.Equal(t, []int{3, 0, 0, 0, 0}, hist["counts"])

	require.EqualValues(t, 3, got["layer_count"])
	require.EqualValues(t, 2, got["archive_layer_count"])
}
