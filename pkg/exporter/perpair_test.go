package exporter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanPair_ClassifiesLayers(t *testing.T) {
	p, err := planPair(context.Background(), Pair{
		Name: "svc", BaselineRef: "../../testdata/fixtures/v1_oci.tar",
		TargetRef: "../../testdata/fixtures/v2_oci.tar",
	}, "linux/amd64")
	require.NoError(t, err)
	require.Equal(t, "svc", p.Name)
	require.NotEmpty(t, p.TargetManifest)
	require.NotEmpty(t, p.Shipped, "v2 differs from v1 by at least one layer")
	require.NotEmpty(t, p.Required, "shared base layer required from baseline")
}
