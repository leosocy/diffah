package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/exporter"
)

// buildInspectTestDelta produces a delta.tar we can inspect.
func buildInspectTestDelta(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	root := ".."
	targetRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)
	baselineRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		TargetRef: targetRef, BaselineRef: baselineRef, OutputPath: out, ToolVersion: "test",
	}))
	return out
}

func TestInspectCommand_PrintsSidecarFields(t *testing.T) {
	delta := buildInspectTestDelta(t)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"inspect", delta})
	require.NoError(t, rootCmd.Execute())

	out := buf.String()
	require.Contains(t, out, "version: v1")
	require.Contains(t, out, "platform:")
	require.Contains(t, out, "target manifest digest:")
	require.Contains(t, out, "baseline manifest digest:")
	require.Contains(t, out, "shipped:")
	require.Contains(t, out, "required:")
	require.Regexp(t, `saved\s+[0-9.]+%\s+vs full image`, out)
}
