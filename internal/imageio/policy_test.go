package imageio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPolicyContext_ReturnsUsableContext(t *testing.T) {
	ctx, err := DefaultPolicyContext()
	require.NoError(t, err)
	require.NotNil(t, ctx)
	require.NoError(t, ctx.Destroy())
}
