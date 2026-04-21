package importer

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

func TestExtractBundle_ParsesSidecar(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()

	require.Equal(t, diff.SchemaVersionV1, b.sidecar.Version)
	require.Equal(t, diff.FeatureBundle, b.sidecar.Feature)
	require.Len(t, b.sidecar.Images, 1)
	require.Equal(t, "svc-a", b.sidecar.Images[0].Name)
}

func TestExtractBundle_RejectsLegacyArchive(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.tar")
	f, err := os.Create(legacyPath)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	hdr := &tar.Header{Name: diff.SidecarFilename, Size: int64(len(`{"version":"v1","tool":"diffah"}`)), Mode: 0o644}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(`{"version":"v1","tool":"diffah"}`))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	f.Close()

	_, err = extractBundle(legacyPath)
	require.Error(t, err)
	var p1 *diff.ErrPhase1Archive
	require.ErrorAs(t, err, &p1, "must reject Phase 1 archive")
}

func buildTestBundle(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         name,
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "off",
		OutputPath:  outPath,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	return outPath
}
