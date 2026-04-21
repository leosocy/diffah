package exporter_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

// repoRoot returns the absolute path to the repository root.
// Tests run with cwd set to the package directory (pkg/exporter/),
// so the repo root is two levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..")
}

func readSidecar(t *testing.T, archivePath string) *diff.Sidecar {
	t.Helper()
	raw, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	return sc
}

func TestExport_OCIFixture_HappyPath(t *testing.T) {
	ctx := context.Background()
	targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar")
	baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar")

	targetRef, err := imageio.ParseReference("oci-archive:" + targetPath)
	require.NoError(t, err)
	baselineRef, err := imageio.ParseReference("oci-archive:" + baselinePath)
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")

	err = exporter.Export(ctx, exporter.Options{
		TargetRef:   targetRef,
		BaselineRef: baselineRef,
		Compress:    "none",
		OutputPath:  out,
		ToolVersion: "test",
	})
	require.NoError(t, err)

	sc := readSidecar(t, out)
	require.Equal(t, diff.SchemaVersionV1, sc.Version)
	require.NotEmpty(t, sc.Platform, "sidecar must record platform")
	require.NotEmpty(t, sc.ShippedInDelta, "delta should ship at least the version layer")
	require.NotEmpty(t, sc.RequiredFromBaseline, "baseline should contribute the shared layer")
}

func TestExport_S2Fixture_HappyPath(t *testing.T) {
	ctx := context.Background()
	targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_s2.tar")
	baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v1_s2.tar")

	targetRef, err := imageio.ParseReference("docker-archive:" + targetPath)
	require.NoError(t, err)
	baselineRef, err := imageio.ParseReference("docker-archive:" + baselinePath)
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	err = exporter.Export(ctx, exporter.Options{
		TargetRef:   targetRef,
		BaselineRef: baselineRef,
		OutputPath:  out,
		Compress:    "none",
		ToolVersion: "test",
	})
	require.NoError(t, err)

	sc := readSidecar(t, out)
	require.NotEmpty(t, sc.Platform)
	require.NotEmpty(t, sc.ShippedInDelta)
	require.NotEmpty(t, sc.RequiredFromBaseline)
}

func TestExport_NoBaselineReturnsError(t *testing.T) {
	ctx := context.Background()
	targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar")
	targetRef, err := imageio.ParseReference("oci-archive:" + targetPath)
	require.NoError(t, err)
	err = exporter.Export(ctx, exporter.Options{
		TargetRef:  targetRef,
		OutputPath: filepath.Join(t.TempDir(), "d.tar"),
	})
	require.Error(t, err)
}

// extractManifestToFile reads the manifest from an image ref and writes it
// to a temp file, returning the path. Used to test manifest-only baseline.
func extractManifestToFile(t *testing.T, imageRef string) string {
	t.Helper()
	ctx := context.Background()
	ref, err := imageio.ParseReference(imageRef)
	require.NoError(t, err)
	src, err := ref.NewImageSource(ctx, nil)
	require.NoError(t, err)
	defer src.Close()
	raw, _, err := src.GetManifest(ctx, nil)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

func TestExport_ManifestOnlyBaseline(t *testing.T) {
	ctx := context.Background()
	baselinePath := extractManifestToFile(t,
		"oci-archive:"+filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))

	out := filepath.Join(t.TempDir(), "delta.tar")
	targetRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)

	err = exporter.Export(ctx, exporter.Options{
		TargetRef:            targetRef,
		BaselineManifestPath: baselinePath,
		OutputPath:           out,
		Compress:             "none",
		ToolVersion:          "test",
		IntraLayer:           "off",
	})
	require.NoError(t, err)

	sc := readSidecar(t, out)
	require.Equal(t, baselinePath, sc.Baseline.SourceHint)
	require.NotEmpty(t, sc.ShippedInDelta)
	require.NotEmpty(t, sc.RequiredFromBaseline)
}

func TestExport_DryRun_DoesNotWriteOutput(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "should-not-exist.tar")

	targetRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)
	baselineRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	stats, err := exporter.DryRun(ctx, exporter.Options{
		TargetRef:   targetRef,
		BaselineRef: baselineRef,
		OutputPath:  out,
		ToolVersion: "test",
	})
	require.NoError(t, err)
	require.Greater(t, stats.ShippedCount, 0)
	require.Greater(t, stats.ShippedBytes, int64(0))
	require.Greater(t, stats.RequiredCount, 0)

	// Output must not exist.
	_, err = os.Stat(out)
	require.True(t, os.IsNotExist(err))
}

func TestExport_DryRun_ManifestOnlyBaseline(t *testing.T) {
	ctx := context.Background()
	baselinePath := extractManifestToFile(t,
		"oci-archive:"+filepath.Join(repoRoot(t), "testdata/fixtures/v1_oci.tar"))

	targetRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)

	stats, err := exporter.DryRun(ctx, exporter.Options{
		TargetRef:            targetRef,
		BaselineManifestPath: baselinePath,
	})
	require.NoError(t, err)
	require.Greater(t, stats.ShippedCount, 0)
}

// TestExport_DeterministicArchive asserts that running the exporter
// twice on identical inputs produces byte-identical archives. Guards
// against regressions in baseline ordering, blob emission order, or
// JSON encoding non-determinism.
func TestExport_DeterministicArchive(t *testing.T) {
	ctx := context.Background()
	targetPath := filepath.Join(repoRoot(t), "testdata/fixtures/v3_oci.tar")
	baselinePath := filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar")

	targetRef, err := imageio.ParseReference("oci-archive:" + targetPath)
	require.NoError(t, err)
	baselineRef, err := imageio.ParseReference("oci-archive:" + baselinePath)
	require.NoError(t, err)

	outDir := t.TempDir()

	run := func(name string) []byte {
		outPath := filepath.Join(outDir, name+".tar")
		fixedTime := time.Date(2026, 4, 20, 13, 21, 0, 0, time.UTC)
		require.NoError(t, exporter.Export(ctx, exporter.Options{
			TargetRef:   targetRef,
			BaselineRef: baselineRef,
			Compress:    "none",
			OutputPath:  outPath,
			ToolVersion: "test",
			CreatedAt:   fixedTime,
		}))
		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		return data
	}

	a := run("a")
	b := run("b")
	require.True(t, bytes.Equal(a, b),
		"two runs on identical inputs must produce byte-identical archives")
}
