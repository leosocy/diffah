package exporter

import (
	"bytes"
	"context"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestEncodeShipped_WarningOnError_FallbackToFull(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ctx := context.Background()

	plan, err := planPair(ctx, Pair{
		Name:         "svc-a",
		BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath:   "../../testdata/fixtures/v2_oci.tar",
	}, "linux/amd64")
	require.NoError(t, err)

	fakeDigest := digest.Digest("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	for i := range plan.BaselineLayerMeta {
		plan.BaselineLayerMeta[i].Digest = fakeDigest
	}

	pool := newBlobPool()
	seedManifestAndConfig(pool, plan)
	for _, s := range plan.Shipped {
		pool.countShipped(s.Digest)
	}

	var progress bytes.Buffer
	err = encodeShipped(ctx, pool, []*pairPlan{plan}, "auto", DefaultFingerprinter{}, &progress)
	require.NoError(t, err, "encodeShipped must tolerate per-layer errors")
	require.Contains(t, progress.String(), "patch encode failed",
		"progress must include a fallback warning")

	for _, s := range plan.Shipped {
		entry, ok := pool.entries[s.Digest]
		require.True(t, ok, "shipped blob must be in pool")
		require.Equal(t, diff.EncodingFull, entry.Encoding, "fallback must be full encoding")
	}
}
