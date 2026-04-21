package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComposeImage_SingleImage(t *testing.T) {
	ctx := context.Background()
	bundlePath := buildTestBundle(t, "svc-a")
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()

	baselines := map[string]string{"svc-a": "../../testdata/fixtures/v1_oci.tar"}
	resolved, err := resolveBaselines(ctx, b.sidecar, baselines, false)
	require.NoError(t, err)

	img := b.sidecar.Images[0]
	ci, err := composeImage(ctx, img, b.sidecar, b, resolved[0].Ref)
	require.NoError(t, err)
	defer ci.cleanup()

	require.NotNil(t, ci.Ref)
	require.Equal(t, "svc-a", ci.Name)
}
