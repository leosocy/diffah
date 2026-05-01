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
