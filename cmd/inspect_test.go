package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

func buildInspectTestDelta(t *testing.T) string {
	t.Skip("rewritten in Task 17")
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
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: baselineRef.StringWithinTransport(), TargetPath: targetRef.StringWithinTransport()}},
		OutputPath:  out,
		ToolVersion: "test",
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

func TestPrintLegacySidecar_IntraLayerStats(t *testing.T) {
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
	err := printLegacySidecar(&buf, "/tmp/test.tar", s)
	require.NoError(t, err)

	out := buf.String()

	require.Contains(t, out, "full: 1")
	require.Contains(t, out, "patch: 2")
	require.Contains(t, out, "total archive: 2000 bytes")
	require.Contains(t, out, "patch savings: 4000 bytes")
	require.Contains(t, out, "avg patch ratio: 20.0%")
	require.Contains(t, out, "version: v1")
	require.Contains(t, out, "tool: diffah")
	require.Contains(t, out, "tool_version: v0.1.0")
	require.Contains(t, out, "platform: linux/amd64")
	require.Contains(t, out, "created_at:")
}

func TestPrintBundleSidecar_PerImageStats(t *testing.T) {
	s := &diff.Sidecar{
		Version:     "v1",
		Feature:     "bundle",
		Tool:        "diffah",
		ToolVersion: "v0.2.0",
		CreatedAt:   time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{
			{
				Name: "service-a",
				Target: diff.TargetRef{
					ManifestDigest: "sha256:manifest-a",
					ManifestSize:   200,
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
				},
				Baseline: diff.BaselineRef{
					ManifestDigest: "sha256:base-a",
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
					SourceHint:     "oci-archive:/tmp/base-a.tar",
				},
			},
			{
				Name: "service-b",
				Target: diff.TargetRef{
					ManifestDigest: "sha256:manifest-b",
					ManifestSize:   300,
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
				},
				Baseline: diff.BaselineRef{
					ManifestDigest: "sha256:base-b",
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
				},
			},
		},
		Blobs: map[digest.Digest]diff.BlobEntry{
			"sha256:manifest-a": {Size: 200, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 200},
			"sha256:manifest-b": {Size: 300, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 300},
			"sha256:layer1":     {Size: 5000, MediaType: "m", Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: "sha256:ref1", ArchiveSize: 1000},
			"sha256:layer2":     {Size: 3000, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 3000},
		},
	}

	var buf bytes.Buffer
	err := printBundleSidecar(&buf, "/tmp/bundle.tar", s)
	require.NoError(t, err)

	out := buf.String()

	require.Contains(t, out, "archive: /tmp/bundle.tar")
	require.Contains(t, out, "feature: bundle")
	require.Contains(t, out, "images: 2")
	require.Contains(t, out, "blobs: 4 (full: 3, patch: 1)")
	require.Contains(t, out, "total archive: 4500 bytes")
	require.Contains(t, out, "avg patch ratio: 20.0%")
	require.Contains(t, out, "patch savings: 4000 bytes (80.0% vs full)")

	require.Contains(t, out, "--- image: service-a ---")
	require.Contains(t, out, "target manifest digest: sha256:manifest-a")
	require.Contains(t, out, "baseline manifest digest: sha256:base-a")
	require.Contains(t, out, "baseline source: oci-archive:/tmp/base-a.tar")

	require.Contains(t, out, "--- image: service-b ---")
	require.Contains(t, out, "target manifest digest: sha256:manifest-b")
	require.Contains(t, out, "baseline manifest digest: sha256:base-b")
}

func TestRunInspect_Phase1Archive_PrintsHint(t *testing.T) {
	ls := &diff.LegacySidecar{
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
			{Digest: "sha256:f1", Size: 100, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 100},
		},
		RequiredFromBaseline: []diff.BlobRef{},
	}

	raw, err := json.Marshal(ls)
	require.NoError(t, err)

	var p1 *diff.ErrPhase1Archive
	_, perr := diff.ParseSidecar(raw)
	require.ErrorAs(t, perr, &p1, "legacy sidecar JSON should trigger ErrPhase1Archive from ParseSidecar")

	parsed, lerr := diff.ParseLegacySidecar(raw)
	require.NoError(t, lerr, "ParseLegacySidecar should succeed on legacy JSON")

	var buf bytes.Buffer
	err = printLegacySidecar(&buf, "/tmp/phase1.tar", parsed)
	require.NoError(t, err)

	require.Contains(t, buf.String(), "version: v1")
}

func TestRunInspect_BundleSidecar_ParsesDirectly(t *testing.T) {
	s := &diff.Sidecar{
		Version:     "v1",
		Feature:     "bundle",
		Tool:        "diffah",
		ToolVersion: "v0.2.0",
		CreatedAt:   time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{
			{
				Name: "svc",
				Target: diff.TargetRef{
					ManifestDigest: "sha256:aa",
					ManifestSize:   5,
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
				},
				Baseline: diff.BaselineRef{
					ManifestDigest: "sha256:bb",
					MediaType:      "application/vnd.oci.image.manifest.v1+json",
				},
			},
		},
		Blobs: map[digest.Digest]diff.BlobEntry{
			"sha256:aa": {Size: 5, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 5},
		},
	}

	raw, err := json.Marshal(s)
	require.NoError(t, err)

	parsed, perr := diff.ParseSidecar(raw)
	require.NoError(t, perr, "ParseSidecar should succeed on bundle JSON")

	var buf bytes.Buffer
	err = printBundleSidecar(&buf, "/tmp/bundle.tar", parsed)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "feature: bundle")
	require.Contains(t, buf.String(), "--- image: svc ---")
}
