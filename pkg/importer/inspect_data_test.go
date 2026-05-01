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

func TestComputeTopSavings_SortsBySavedBytesDesc(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("small"), Kind: LayerKindPatch, TargetSize: 100, ArchiveSize: 50}, // saved 50
		{Digest: d("big"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100}, // saved 900
		{Digest: d("mid"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 100},  // saved 400
	}

	top := computeTopSavings(rows, 10)
	require.Len(t, top, 3)
	require.Equal(t, d("big"), top[0].Digest)
	require.EqualValues(t, 900, top[0].SavedBytes)
	require.InDelta(t, 0.9, top[0].SavedRatio, 0.001)
	require.Equal(t, d("mid"), top[1].Digest)
	require.Equal(t, d("small"), top[2].Digest)
}

func TestComputeTopSavings_OmitsZeroSavingRowsAndCapsAtN(t *testing.T) {
	var rows []LayerRow
	for i := 0; i < 15; i++ {
		rows = append(rows, LayerRow{
			Digest: d(string(rune('a' + i))), Kind: LayerKindPatch,
			TargetSize: 100, ArchiveSize: int64(100 - i), // i=0 saves 0; i=1..14 save 1..14
		})
	}

	top := computeTopSavings(rows, 10)
	require.Len(t, top, 10)
	require.EqualValues(t, 14, top[0].SavedBytes)
	require.EqualValues(t, 5, top[9].SavedBytes)
}

func TestComputeTopSavings_TieBreakerByDigestLexicographic(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("zzz"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60}, // saved 40
		{Digest: d("aaa"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60}, // saved 40
		{Digest: d("mmm"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60}, // saved 40
	}
	top := computeTopSavings(rows, 10)
	require.Equal(t, d("aaa"), top[0].Digest)
	require.Equal(t, d("mmm"), top[1].Digest)
	require.Equal(t, d("zzz"), top[2].Digest)
}

func TestComputeTopSavings_BaselineOnlyContributesFullTargetSize(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("base"), Kind: LayerKindBaselineOnly, TargetSize: 1000},
		{Digest: d("patch"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 200},
	}
	top := computeTopSavings(rows, 10)
	require.Equal(t, d("base"), top[0].Digest)
	require.EqualValues(t, 1000, top[0].SavedBytes)
}
