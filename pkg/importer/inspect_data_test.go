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
