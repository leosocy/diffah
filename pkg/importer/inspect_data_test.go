package importer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestComputeHistogram_BucketBoundariesAreHalfOpen(t *testing.T) {
	const (
		MiB = 1 << 20
		GiB = 1 << 30
	)
	rows := []LayerRow{
		{Digest: d("a"), TargetSize: 0},           // < 1 MiB
		{Digest: d("b"), TargetSize: MiB - 1},     // < 1 MiB
		{Digest: d("c"), TargetSize: MiB},         // 1–10 MiB (lower-inclusive)
		{Digest: d("d"), TargetSize: 10 * MiB},    // 10–100 MiB (lower-inclusive)
		{Digest: d("e"), TargetSize: 100*MiB - 1}, // 10–100 MiB (upper-exclusive)
		{Digest: d("f"), TargetSize: 100 * MiB},   // 100 MiB–1 GiB (lower-inclusive)
		{Digest: d("g"), TargetSize: GiB},         // ≥ 1 GiB
	}
	h := computeHistogram(rows)
	require.Equal(t, []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}, h.Buckets)
	require.Equal(t, []int{2, 1, 2, 1, 1}, h.Counts)
}

func TestComputeHistogram_EmptyInputProducesAllZero(t *testing.T) {
	h := computeHistogram(nil)
	require.Equal(t, []int{0, 0, 0, 0, 0}, h.Counts)
}

const ociMediaType = "application/vnd.oci.image.manifest.v1+json"

// fakeOCIManifest builds a minimal OCI v1 manifest JSON with the given layers.
func fakeOCIManifest(t *testing.T, layers []LayerRef) []byte {
	t.Helper()
	type layer struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}
	body := struct {
		SchemaVersion int     `json:"schemaVersion"`
		MediaType     string  `json:"mediaType"`
		Config        layer   `json:"config"`
		Layers        []layer `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     ociMediaType,
		Config:        layer{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:" + strings.Repeat("c", 64), Size: 10},
	}
	for _, l := range layers {
		body.Layers = append(body.Layers, layer{
			MediaType: "application/vnd.oci.image.layer.v1.tar",
			Digest:    string(l.Digest),
			Size:      l.Size,
		})
	}
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

func TestBuildInspectImageDetail_EndToEnd(t *testing.T) {
	manifestLayers := []LayerRef{
		{Digest: d(strings.Repeat("a", 64)), Size: 12 * (1 << 20)}, // Full
		{Digest: d(strings.Repeat("b", 64)), Size: 8 * (1 << 20)},  // Patch
		{Digest: d(strings.Repeat("c", 64)), Size: 5 * (1 << 20)},  // baseline-only
	}
	mfBytes := fakeOCIManifest(t, manifestLayers)
	mfDigest := digest.FromBytes(mfBytes)

	sc := &diff.Sidecar{
		Version: diff.SchemaVersionV1, Feature: diff.FeatureBundle, Tool: "diffah",
		ToolVersion: "test", CreatedAt: time.Now(), Platform: "linux/amd64",
		Images: []diff.ImageEntry{{
			Name:     "svc",
			Target:   diff.TargetRef{ManifestDigest: mfDigest, ManifestSize: int64(len(mfBytes)), MediaType: ociMediaType},
			Baseline: diff.BaselineRef{ManifestDigest: digest.Digest("sha256:" + strings.Repeat("0", 64)), MediaType: ociMediaType},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest:                   {Size: int64(len(mfBytes)), MediaType: ociMediaType, Encoding: diff.EncodingFull, ArchiveSize: int64(len(mfBytes))},
			d(strings.Repeat("a", 64)): {Size: 12 * (1 << 20), Encoding: diff.EncodingFull, ArchiveSize: 12 * (1 << 20)},
			d(strings.Repeat("b", 64)): {Size: 8 * (1 << 20), Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: d(strings.Repeat("9", 64)), ArchiveSize: 500 * 1024},
			// c absent → baseline-only
		},
	}

	detail, err := BuildInspectImageDetail(sc, sc.Images[0], mfBytes)
	require.NoError(t, err)
	require.Equal(t, "svc", detail.Name)
	require.Equal(t, mfDigest, detail.ManifestDigest)
	require.Equal(t, 3, detail.LayerCount)
	require.Equal(t, 2, detail.ArchiveLayerCount)
	require.Len(t, detail.Layers, 3)
	require.Equal(t, LayerKindFull, detail.Layers[0].Kind)
	require.Equal(t, LayerKindPatch, detail.Layers[1].Kind)
	require.Equal(t, LayerKindBaselineOnly, detail.Layers[2].Kind)
	require.Empty(t, detail.Waste)
	require.NotEmpty(t, detail.TopSavings)
	require.Equal(t, []int{0, 2, 1, 0, 0}, detail.Histogram.Counts)
}

func TestBuildInspectImageDetail_RejectsManifestList(t *testing.T) {
	sc := &diff.Sidecar{
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{
				ManifestDigest: d("00"), MediaType: "application/vnd.oci.image.index.v1+json",
			},
		}},
	}
	_, err := BuildInspectImageDetail(sc, sc.Images[0], []byte(`{"schemaVersion":2,"manifests":[]}`))
	require.Error(t, err)
}
