package importer_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func buildDeltaIntraLayerAuto(t *testing.T, transport, targetTar, baselineTar string) string {
	t.Skip("rewritten in Task 17")
	t.Helper()
	ctx := context.Background()
	target, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", targetTar))
	require.NoError(t, err)
	baseline, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", baselineTar))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: baseline.StringWithinTransport(), TargetPath: target.StringWithinTransport()}},
		OutputPath:  out,
		ToolVersion: "test",
		IntraLayer:  "auto",
	}))
	return out
}

func TestIntraLayer_E2E_OCI_ExportImportRoundTrip(t *testing.T) {
	t.Skip("rewritten in Task 25")
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}
	ctx := context.Background()

	delta := buildDeltaIntraLayerAuto(t, "oci-archive", "v3_oci.tar", "v2_oci.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	hasPatch := false
	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			hasPatch = true
			require.NotEmpty(t, e.Codec, "patch entry must declare a codec")
			require.NotEmpty(t, e.PatchFromDigest, "patch entry must reference a baseline blob")
			require.Greater(t, e.ArchiveSize, int64(0), "patch archive_size must be > 0")
			require.Less(t, e.ArchiveSize, e.Size, "patch archive_size must be < original size")
		}
	}
	require.True(t, hasPatch, "v3→v2 delta with intra-layer=auto must produce at least one patch entry")

	baselineRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v3_reconstructed.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath: delta,
		Baselines: map[string]string{"default": baselineRef.StringWithinTransport()},
		OutputPath: out,
	}))

	outRef, err := imageio.ParseReference("oci-archive:" + out)
	require.NoError(t, err)
	gotDigest := readManifestDigest(ctx, t, outRef)
	require.Equal(t, sidecar.Target.ManifestDigest, gotDigest,
		"imported image manifest digest must match original target")
}

func TestIntraLayer_E2E_S2_ExportImportRoundTrip(t *testing.T) {
	t.Skip("rewritten in Task 25")
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}
	ctx := context.Background()

	delta := buildDeltaIntraLayerAuto(t, "docker-archive", "v3_s2.tar", "v2_s2.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	hasPatch := false
	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			hasPatch = true
			require.NotEmpty(t, e.Codec)
			require.NotEmpty(t, e.PatchFromDigest)
			require.Greater(t, e.ArchiveSize, int64(0))
			require.Less(t, e.ArchiveSize, e.Size)
		}
	}
	require.True(t, hasPatch, "v3→v2 schema2 delta with intra-layer=auto must produce at least one patch entry")

	baselineRef, err := imageio.ParseReference(
		"docker-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_s2.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v3_reconstructed.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath: delta,
		Baselines: map[string]string{"default": baselineRef.StringWithinTransport()},
		OutputPath: out,
	}))

	outRef, err := imageio.ParseReference("docker-archive:" + out)
	require.NoError(t, err)
	gotDigest := readManifestDigest(ctx, t, outRef)
	require.Equal(t, sidecar.Target.ManifestDigest, gotDigest,
		"imported image manifest digest must match original target")
}

func TestIntraLayer_E2E_PatchSmallerThanFull(t *testing.T) {
	t.Skip("rewritten in Task 25")
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}

	delta := buildDeltaIntraLayerAuto(t, "oci-archive", "v3_oci.tar", "v2_oci.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			ratio := float64(e.ArchiveSize) / float64(e.Size)
			require.Less(t, ratio, 0.5,
				"patch encoding for a 1-byte-diff layer should be < 50%% of original; got %.2f%%", ratio*100)
		}
	}
}
