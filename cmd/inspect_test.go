package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

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
	err := printBundleSidecar(&buf, "/tmp/bundle.tar", s, true, true, nil)
	require.NoError(t, err)

	out := buf.String()

	require.Contains(t, out, "archive: /tmp/bundle.tar")
	require.Contains(t, out, "feature: bundle")
	require.Contains(t, out, "images: 2")
	require.Contains(t, out, "blobs: 4 (full: 3, patch: 1)")
	require.Contains(t, out, "total archive: 4500 bytes")
	require.Contains(t, out, "avg patch ratio: 20.0%")
	require.Contains(t, out, "patch savings: 4000 bytes (80.0% vs full)")
	require.Contains(t, out, "intra-layer patches required: yes")
	require.Contains(t, out, "zstd available: yes")

	require.Contains(t, out, "--- image: service-a ---")
	require.Contains(t, out, "target manifest digest: sha256:manifest-a")
	require.Contains(t, out, "baseline manifest digest: sha256:base-a")
	require.Contains(t, out, "baseline source: oci-archive:/tmp/base-a.tar")

	require.Contains(t, out, "--- image: service-b ---")
	require.Contains(t, out, "target manifest digest: sha256:manifest-b")
	require.Contains(t, out, "baseline manifest digest: sha256:base-b")
}

func TestRunInspect_Phase1Archive_PrintsHint(t *testing.T) {
	legacyJSON := map[string]interface{}{
		"version":                "v1",
		"tool":                   "diffah",
		"tool_version":           "v0.1.0",
		"created_at":             "2026-04-20T10:00:00Z",
		"platform":               "linux/amd64",
		"target":                 map[string]string{"manifest_digest": "sha256:aaa", "media_type": "m"},
		"baseline":               map[string]string{"manifest_digest": "sha256:bbb", "media_type": "m"},
		"required_from_baseline": []interface{}{},
		"shipped_in_delta":       []interface{}{},
	}
	raw, err := json.Marshal(legacyJSON)
	require.NoError(t, err)

	var p1 *diff.ErrPhase1Archive
	_, perr := diff.ParseSidecar(raw)
	require.ErrorAs(t, perr, &p1, "legacy sidecar JSON should trigger ErrPhase1Archive from ParseSidecar")
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
	err = printBundleSidecar(&buf, "/tmp/bundle.tar", parsed, false, false, nil)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "feature: bundle")
	require.Contains(t, buf.String(), "--- image: svc ---")
}

func TestPrintBundleSidecar_AppendsPerImageSections(t *testing.T) {
	mfDigest := digest.Digest("sha256:" + strings.Repeat("a", 64))
	s := &diff.Sidecar{
		Version:     "v1",
		Feature:     "bundle",
		Tool:        "diffah",
		ToolVersion: "v0.x",
		CreatedAt:   time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{
				ManifestDigest: mfDigest, ManifestSize: 100,
				MediaType: "application/vnd.oci.image.manifest.v1+json",
			},
			Baseline: diff.BaselineRef{
				ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)),
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		},
	}
	details := map[string]importer.InspectImageDetail{
		"svc": {
			Name: "svc", ManifestDigest: mfDigest,
			LayerCount: 1, ArchiveLayerCount: 1,
			Layers: []importer.LayerRow{
				{Digest: digest.Digest("sha256:" + strings.Repeat("c", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			},
			Histogram: importer.SizeHistogram{
				Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
				Counts:  []int{1, 0, 0, 0, 0},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, printBundleSidecar(&buf, "/tmp/bundle.tar", s, false, false, details))
	out := buf.String()

	require.Contains(t, out, "archive: /tmp/bundle.tar")
	require.Contains(t, out, "feature: bundle")
	require.Contains(t, out, "--- image: svc ---")
	require.Contains(t, out, "Layers (target manifest order):")
	require.Contains(t, out, "Waste:")
	require.Contains(t, out, "Layer-size histogram (target bytes):")
}
