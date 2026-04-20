package exporter_test

import (
	"context"
	"path/filepath"
	"testing"

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
