package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
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
		TargetRef: targetRef, LegacyBaselineRef: baselineRef, OutputPath: out, ToolVersion: "test",
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
	require.Contains(t, out, "shipped blobs:")
	require.Contains(t, out, "full:")
	require.Contains(t, out, "total archive:")
	require.Contains(t, out, "required:")
	require.Regexp(t, `saved\s+[0-9.]+%\s+vs full image`, out)
}

func TestPrintSidecar_IntraLayerStats(t *testing.T) {
	s := &diff.LegacySidecar{
		Version:     "v1",
		Tool:        "diffah",
		ToolVersion: "v0.1.0",
		CreatedAt:   time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Target: diff.LegacyTargetRef{
			ManifestDigest: digest.Digest("sha256:aaa"),
			ManifestSize:   100,
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
		},
		Baseline: diff.LegacyBaselineRef{
			ManifestDigest: digest.Digest("sha256:bbb"),
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
		},
		ShippedInDelta: []diff.BlobRef{
			{
				Digest:      "sha256:full1",
				Size:        1000,
				MediaType:   "m",
				Encoding:    diff.EncodingFull,
				ArchiveSize: 1000,
			},
			{
				Digest:          "sha256:patch1",
				Size:            2000,
				MediaType:       "m",
				Encoding:        diff.EncodingPatch,
				Codec:           "zstd-patch",
				PatchFromDigest: "sha256:ref1",
				ArchiveSize:     400,
			},
			{
				Digest:          "sha256:patch2",
				Size:            3000,
				MediaType:       "m",
				Encoding:        diff.EncodingPatch,
				Codec:           "zstd-patch",
				PatchFromDigest: "sha256:ref2",
				ArchiveSize:     600,
			},
		},
		RequiredFromBaseline: []diff.BlobRef{
			{Digest: "sha256:base1", Size: 500, MediaType: "m"},
		},
	}

	var buf bytes.Buffer
	err := printSidecar(&buf, "/tmp/test.tar", s)
	require.NoError(t, err)

	out := buf.String()

	// Verify new intra-layer fields.
	require.Contains(t, out, "full: 1")
	require.Contains(t, out, "patch: 2")
	require.Contains(t, out, "total archive: 2000 bytes")

	// patch savings = (2000+3000) - (400+600) = 4000 bytes
	require.Contains(t, out, "patch savings: 4000 bytes")

	// avg patch ratio = (400+600) / (2000+3000) * 100 = 20.0%
	require.Contains(t, out, "avg patch ratio: 20.0%")

	// Also check existing fields still present.
	require.Contains(t, out, "version: v1")
	require.Contains(t, out, "tool: diffah")
	require.Contains(t, out, "tool_version: v0.1.0")
	require.Contains(t, out, "platform: linux/amd64")
	require.Contains(t, out, "created_at:")
}
