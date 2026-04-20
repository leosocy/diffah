package importer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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
	ctx := context.Background()
	target, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures", targetTar))
	require.NoError(t, err)
	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures", baselineTar))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		TargetRef: target, BaselineRef: baseline, OutputPath: out, ToolVersion: "test",
	}))
	return out
}

func TestImport_RoundTrip_OCIFixture(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	baseline, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v2.tar")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:    delta,
		BaselineRef:  baseline,
		OutputPath:   out,
		OutputFormat: "docker-archive",
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
		DeltaPath:    delta,
		BaselineRef:  baseline,
		OutputPath:   out,
		OutputFormat: "dir",
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
		DeltaPath:    delta,
		BaselineRef:  baseline,
		OutputPath:   filepath.Join(t.TempDir(), "out"),
		OutputFormat: "bogus",
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
		DeltaPath:    delta,
		BaselineRef:  unrelated,
		OutputPath:   out,
		OutputFormat: "docker-archive",
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
		DeltaPath:    delta,
		BaselineRef:  baselineRef,
		OutputPath:   out,
		OutputFormat: "docker-archive",
	})
	require.NoError(t, err)
	require.True(t, report.AllReachable)
	require.Greater(t, report.RequiredBlobs, 0)
	require.Empty(t, report.MissingDigests)

	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

func TestImport_DryRun_OnlyProbes_Missing(t *testing.T) {
	ctx := context.Background()
	delta := buildDelta(t, "v2_oci.tar", "v1_oci.tar")

	unrelated, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/unrelated_oci.tar"))
	require.NoError(t, err)

	report, err := importer.DryRun(ctx, importer.Options{
		DeltaPath:    delta,
		BaselineRef:  unrelated,
		OutputPath:   filepath.Join(t.TempDir(), "x.tar"),
		OutputFormat: "docker-archive",
	})
	require.NoError(t, err)
	require.False(t, report.AllReachable)
	require.NotEmpty(t, report.MissingDigests)
}
