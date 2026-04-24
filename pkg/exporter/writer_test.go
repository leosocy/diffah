package exporter

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestWriteBundleArchive(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "svc-a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	for _, s := range p1.Shipped {
		pool.countShipped(s.Digest)
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1}, "off", nil, nil))
	sc := assembleSidecar(pool, []*pairPlan{p1}, "linux/amd64", "test", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	outPath := filepath.Join(t.TempDir(), "bundle.tar")
	require.NoError(t, writeBundleArchive(outPath, sc, pool))

	f, err := os.Open(outPath)
	require.NoError(t, err)
	defer f.Close()
	tr := tar.NewReader(f)
	found := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		found[hdr.Name] = true
		_, _ = io.ReadAll(tr)
	}
	require.True(t, found[diff.SidecarFilename], "must contain diffah.json")
	for _, d := range pool.sortedDigests() {
		require.True(t, found[blobPath(d)], "must contain blob %s", d)
	}
}
