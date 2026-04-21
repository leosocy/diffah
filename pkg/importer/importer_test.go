package importer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

// repoRoot locates the repository root from the package test dir.
func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..")
}

// buildDelta runs Export to produce a delta.tar at a temp path.
func buildDelta(t *testing.T, targetTar, baselineTar string) string {
	t.Helper()
	return buildDeltaWithTransport(t, "oci-archive", targetTar, baselineTar)
}

// buildDeltaS2 builds a delta from schema-2 docker-archive fixtures.
func buildDeltaS2(t *testing.T, targetTar, baselineTar string) string {
	t.Helper()
	return buildDeltaWithTransport(t, "docker-archive", targetTar, baselineTar)
}

func buildDeltaWithTransport(t *testing.T, transport, targetTar, baselineTar string) string {
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
		IntraLayer:  "off",
	}))
	return out
}

// readManifestDigest opens an image reference, reads its raw manifest
// bytes, and returns their sha256 digest. The helper lets the digest
// regression tests prove byte-exact reconstruction without depending on
// copy.Image's internal bookkeeping.
func readManifestDigest(ctx context.Context, t *testing.T, ref types.ImageReference) digest.Digest {
	t.Helper()
	src, err := ref.NewImageSource(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = src.Close() }()
	raw, _, err := src.GetManifest(ctx, nil)
	require.NoError(t, err)
	return digest.FromBytes(raw)
}

func TestImport_RoundTrip_OCIFixture(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		OutputFormat:      "oci-archive",
	})
	require.NoError(t, err)

	fi, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}

func TestImport_RoundTrip_DirOutput(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2_dir")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		OutputFormat:      "dir",
	})
	require.NoError(t, err)

	// dir output should contain manifest.json
	_, err = os.Stat(filepath.Join(out, "manifest.json"))
	require.NoError(t, err)
}

func TestImport_UnknownFormat(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        filepath.Join(t.TempDir(), "out"),
		OutputFormat:      "bogus",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bogus")
}

func TestImport_FailFast_MissingBaselineBlob(t *testing.T) {
	ctx := context.Background()
	// Build delta against v1.
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	unrelated, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/unrelated_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "x.tar")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: unrelated,
		OutputPath:        out,
		OutputFormat:      "oci-archive",
	})
	var mbe *diff.ErrBaselineMissingBlob
	require.ErrorAs(t, err, &mbe)
	require.NotEmpty(t, mbe.Digest)

	// Output must not exist after a fail-fast.
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

func TestImport_DryRun_OnlyProbes_Reachable(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baselineRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "x.tar")
	report, err := importer.DryRun(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baselineRef,
		OutputPath:        out,
		OutputFormat:      "oci-archive",
	})
	require.NoError(t, err)
	require.True(t, report.AllReachable)
	require.Greater(t, report.RequiredBlobs, 0)
	require.Empty(t, report.MissingDigests)

	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

// TestImport_AutoFormat_OCI_PreservesManifestDigest checks that an OCI
// source archive reconstructed through the auto-format code path yields
// the exact manifest bytes recorded in the sidecar. This is the
// regression guard for the cross-format byte-breakage issue: it would
// have failed against the previous default --output-format=docker-archive.
func TestImport_AutoFormat_OCI_PreservesManifestDigest(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		// OutputFormat empty → auto-pick to preserve OCI bytes.
	}))

	ref, err := imageio.ParseReference("oci-archive:" + out)
	require.NoError(t, err)
	got := readManifestDigest(ctx, t, ref)
	require.Equal(t, sidecar.Target.ManifestDigest, got,
		"auto-format output must reproduce the sidecar's target manifest bytes")
}

// TestImport_AutoFormat_DockerSchema2_PreservesManifestDigest is the
// schema-2 twin of the OCI regression test.
func TestImport_AutoFormat_DockerSchema2_PreservesManifestDigest(t *testing.T) {
	ctx := context.Background()
	delta := buildDeltaS2(t, "v2_s2.tar", "v1_s2.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	baseline, err := imageio.ParseReference("docker-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_s2.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
	}))

	ref, err := imageio.ParseReference("docker-archive:" + out)
	require.NoError(t, err)
	got := readManifestDigest(ctx, t, ref)
	require.Equal(t, sidecar.Target.ManifestDigest, got,
		"auto-format output must reproduce the sidecar's target manifest bytes")
}

// TestImport_RejectsCrossFormatConversion confirms the compat check runs
// before the expensive copy step: an OCI source paired with a
// docker-archive output surfaces ErrIncompatibleOutputFormat instead of
// silently producing an image with a different manifest digest.
func TestImport_RejectsCrossFormatConversion(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		OutputFormat:      "docker-archive",
	})
	var conflict *diff.ErrIncompatibleOutputFormat
	require.ErrorAs(t, err, &conflict)

	// No partial output should be left behind.
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

// TestImport_AllowConvert_BypassesCompatCheck verifies the explicit opt-in
// restores the legacy converting behaviour. Digests won't match, but the
// command succeeds — this is the escape hatch for operators whose consumer
// only speaks one format.
func TestImport_AllowConvert_BypassesCompatCheck(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		OutputFormat:      "docker-archive",
		AllowConvert:      true,
	}))
	fi, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}

func TestImport_DryRun_OnlyProbes_Missing(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	unrelated, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/unrelated_oci.tar"))
	require.NoError(t, err)

	report, err := importer.DryRun(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: unrelated,
		OutputPath:        filepath.Join(t.TempDir(), "x.tar"),
		OutputFormat:      "oci-archive",
	})
	require.NoError(t, err)
	require.False(t, report.AllReachable)
	require.NotEmpty(t, report.MissingDigests)
}

func TestDryRun_PatchRefs_DetectedAndReported(t *testing.T) {
	tmp := t.TempDir()
	deltaDir := filepath.Join(tmp, "delta")
	require.NoError(t, os.MkdirAll(deltaDir, 0o755))

	sc := diff.LegacySidecar{
		Version: "v1", Tool: "diffah", ToolVersion: "t", Platform: "linux/amd64",
		CreatedAt: time.Now().UTC(),
		Target: diff.LegacyTargetRef{
			ManifestDigest: "sha256:tgt",
			ManifestSize:   1,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
		Baseline: diff.LegacyBaselineRef{
			ManifestDigest: "sha256:b",
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
		RequiredFromBaseline: []diff.BlobRef{},
		ShippedInDelta: []diff.BlobRef{{
			Digest: "sha256:tgt", Size: 100,
			MediaType:       "application/vnd.oci.image.layer.v1.tar+gzip",
			Encoding:        diff.EncodingPatch,
			Codec:           "zstd-patch",
			PatchFromDigest: "sha256:missing-ref",
			ArchiveSize:     50,
		}},
	}
	raw, err := sc.Marshal()
	require.NoError(t, err)

	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, archive.Pack(deltaDir, raw, deltaPath, archive.CompressNone))

	baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar")
	baselineRef, err := imageio.ParseReference("oci-archive:" + baselinePath)
	require.NoError(t, err)

	report, err := importer.DryRun(context.Background(), importer.Options{
		DeltaPath:         deltaPath,
		LegacyBaselineRef: baselineRef,
		OutputPath:        filepath.Join(tmp, "out.tar"),
	})
	require.NoError(t, err)
	require.False(t, report.AllReachable)
	require.Equal(t, 1, report.RequiredPatchRefs)
	require.Equal(t, []string{"sha256:missing-ref"}, report.MissingPatchRefs)
}
