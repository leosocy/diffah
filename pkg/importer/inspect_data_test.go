package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func d(s string) digest.Digest { return digest.Digest("sha256:" + s) }

func TestBuildLayerRows_ClassifiesFullPatchAndBaselineOnly(t *testing.T) {
	manifestLayers := []LayerRef{
		{Digest: d("aaa"), Size: 100},
		{Digest: d("bbb"), Size: 200},
		{Digest: d("ccc"), Size: 300},
	}
	blobs := map[digest.Digest]diff.BlobEntry{
		d("aaa"): {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		d("bbb"): {Size: 200, Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: d("ref1"), ArchiveSize: 50},
		// ccc absent → baseline-only
	}

	rows := buildLayerRows(manifestLayers, blobs)
	require.Len(t, rows, 3)

	require.Equal(t, LayerKindFull, rows[0].Kind)
	require.EqualValues(t, 100, rows[0].TargetSize)
	require.EqualValues(t, 100, rows[0].ArchiveSize)
	require.EqualValues(t, 0, rows[0].SavedBytes())

	require.Equal(t, LayerKindPatch, rows[1].Kind)
	require.EqualValues(t, 200, rows[1].TargetSize)
	require.EqualValues(t, 50, rows[1].ArchiveSize)
	require.Equal(t, d("ref1"), rows[1].PatchFrom)
	require.EqualValues(t, 150, rows[1].SavedBytes())

	require.Equal(t, LayerKindBaselineOnly, rows[2].Kind)
	require.EqualValues(t, 300, rows[2].TargetSize)
	require.EqualValues(t, 0, rows[2].ArchiveSize)
	require.EqualValues(t, 300, rows[2].SavedBytes())
}

func TestDetectWaste_PatchOversizedFlagsArchiveAtOrAboveTarget(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("ok"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100},  // healthy
		{Digest: d("eq"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 500},   // archive == target → waste
		{Digest: d("over"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 600}, // archive > target → waste
		{Digest: d("full"), Kind: LayerKindFull, TargetSize: 500, ArchiveSize: 500},  // Full, not waste
		{Digest: d("base"), Kind: LayerKindBaselineOnly, TargetSize: 500, ArchiveSize: 0},
	}

	w := detectWaste(rows)
	require.Len(t, w, 2)

	require.Equal(t, WasteKindPatchOversized, w[0].Kind)
	require.Equal(t, d("eq"), w[0].Digest)
	require.EqualValues(t, 500, w[0].ArchiveSize)
	require.EqualValues(t, 500, w[0].TargetSize)

	require.Equal(t, d("over"), w[1].Digest)
}

func TestDetectWaste_NoneWhenAllPatchesProfitable(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("a"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100},
		{Digest: d("b"), Kind: LayerKindPatch, TargetSize: 2000, ArchiveSize: 200},
	}
	require.Empty(t, detectWaste(rows))
}
