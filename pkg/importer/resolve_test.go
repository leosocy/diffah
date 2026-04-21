package importer

import (
	"context"
	"testing"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/stretchr/testify/require"
)

func TestResolveBaselines_HappyPath(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()

	baselines := map[string]string{
		"svc-a": "../../testdata/fixtures/v1_oci.tar",
	}
	result, err := resolveBaselines(context.Background(), b.sidecar, baselines, false)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, "svc-a", result[0].Name)
}

func TestResolveBaselines_StrictRejectsMissing(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()

	_, err = resolveBaselines(context.Background(), b.sidecar, map[string]string{}, true)
	require.Error(t, err)
}

func TestResolveBaselines_MismatchDigest(t *testing.T) {
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()

	baselines := map[string]string{
		"svc-a": "../../testdata/fixtures/v2_oci.tar",
	}
	_, err = resolveBaselines(context.Background(), b.sidecar, baselines, false)
	require.Error(t, err)
	var mismatch *diff.ErrBaselineMismatch
	require.ErrorAs(t, err, &mismatch, "wrong baseline must produce ErrBaselineMismatch")
	require.Equal(t, "svc-a", mismatch.Name)
}
