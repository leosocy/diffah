package exporter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestAssembleSidecar_Minimal(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "svc-a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	for _, s := range p1.Shipped {
		pool.countShipped(s.Digest)
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1}, "off", nil, nil))

	sc := assembleSidecar(pool, []*pairPlan{p1}, "linux/amd64", "test", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.Equal(t, diff.SchemaVersionV1, sc.Version)
	require.Equal(t, diff.FeatureBundle, sc.Feature)
	require.Len(t, sc.Images, 1)
	require.Equal(t, "svc-a", sc.Images[0].Name)
	require.NotEmpty(t, sc.Blobs)

	raw, err := sc.Marshal()
	require.NoError(t, err, "assembled sidecar must validate")
	require.Contains(t, string(raw), `"feature": "bundle"`)
}
