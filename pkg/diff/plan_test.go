package diff

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestComputePlan_PartitionsLayers(t *testing.T) {
	baseline := []digest.Digest{"sha256:a", "sha256:b", "sha256:c"}
	target := []BlobRef{
		{Digest: "sha256:b", Size: 10, MediaType: "m"},
		{Digest: "sha256:d", Size: 20, MediaType: "m"},
	}

	p := ComputePlan(target, baseline)

	require.Len(t, p.ShippedInDelta, 1)
	require.Equal(t, digest.Digest("sha256:d"), p.ShippedInDelta[0].Digest)

	require.Len(t, p.RequiredFromBaseline, 1)
	require.Equal(t, digest.Digest("sha256:b"), p.RequiredFromBaseline[0].Digest)
}

func TestComputePlan_EmptyBaselineShipsAll(t *testing.T) {
	target := []BlobRef{
		{Digest: "sha256:a", Size: 1},
		{Digest: "sha256:b", Size: 2},
	}
	p := ComputePlan(target, nil)
	require.Len(t, p.ShippedInDelta, 2)
	require.Empty(t, p.RequiredFromBaseline)
}

func TestComputePlan_PreservesTargetOrderWithinShippedPartition(t *testing.T) {
	target := []BlobRef{
		{Digest: "sha256:x", Size: 1},
		{Digest: "sha256:a", Size: 2},
		{Digest: "sha256:m", Size: 3},
	}
	baseline := []digest.Digest{"sha256:a"}
	p := ComputePlan(target, baseline)

	require.Equal(t, digest.Digest("sha256:x"), p.ShippedInDelta[0].Digest)
	require.Equal(t, digest.Digest("sha256:m"), p.ShippedInDelta[1].Digest)
}

func TestComputePlan_AllLayersInBaseline(t *testing.T) {
	target := []BlobRef{
		{Digest: "sha256:a", Size: 1},
		{Digest: "sha256:b", Size: 2},
	}
	baseline := []digest.Digest{"sha256:a", "sha256:b"}
	p := ComputePlan(target, baseline)
	require.Empty(t, p.ShippedInDelta)
	require.Len(t, p.RequiredFromBaseline, 2)
}

// TestComputePlan_ReturnsNonNilSlicesEvenWhenPartitionEmpty guards against a
// regression where a zero-overlap partition left one of the Plan slices as
// a nil slice, which the sidecar validator then rejected as "field missing".
// Observed against a production image where every layer was replaced by a
// base-image rebase; ComputePlan must still produce a marshal-able LegacySidecar.
func TestComputePlan_ReturnsNonNilSlicesEvenWhenPartitionEmpty(t *testing.T) {
	// Case 1: zero baseline overlap → RequiredFromBaseline must be non-nil.
	targetOnly := []BlobRef{{Digest: "sha256:a", Size: 1}}
	p := ComputePlan(targetOnly, nil)
	require.NotNil(t, p.RequiredFromBaseline,
		"RequiredFromBaseline must be an allocated empty slice, not nil, "+
			"so the sidecar marshals to [] rather than null")

	// Case 2: every target layer in baseline → ShippedInDelta must be non-nil.
	baseline := []digest.Digest{"sha256:a"}
	p2 := ComputePlan(targetOnly, baseline)
	require.NotNil(t, p2.ShippedInDelta,
		"ShippedInDelta must be an allocated empty slice when nothing ships")
}
