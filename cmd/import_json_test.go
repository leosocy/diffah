package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/importer"
)

func TestImportDryRunJSON_Structure(t *testing.T) {
	report := importer.DryRunReport{
		Feature:       "bundle",
		Version:       "v1",
		Tool:          "diffah",
		ToolVersion:   "0.3.0",
		CreatedAt:     time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
		Platform:      "linux/amd64",
		ArchiveBytes:  98765,
		RequiresZstd:  true,
		ZstdAvailable: true,
		Blobs: importer.BlobStats{
			FullCount:  3,
			PatchCount: 2,
			FullBytes:  50000,
			PatchBytes: 12000,
		},
		Images: []importer.ImageDryRun{
			{
				Name:                   "svc-a",
				BaselineManifestDigest: digest.Digest("sha256:base-a"),
				TargetManifestDigest:   digest.Digest("sha256:target-a"),
				BaselineProvided:       true,
				WouldImport:            true,
				LayerCount:             5,
				ArchiveLayerCount:      3,
				BaselineLayerCount:     2,
				PatchLayerCount:        1,
			},
			{
				Name:                   "svc-b",
				BaselineManifestDigest: digest.Digest("sha256:base-b"),
				TargetManifestDigest:   digest.Digest("sha256:target-b"),
				BaselineProvided:       false,
				WouldImport:            false,
				SkipReason:             "no baseline provided",
				LayerCount:             4,
				ArchiveLayerCount:      4,
				BaselineLayerCount:     0,
				PatchLayerCount:        1,
			},
		},
	}

	result := importDryRunJSON(report)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Feature       string `json:"feature"`
			Version       string `json:"version"`
			Tool          string `json:"tool"`
			ToolVersion   string `json:"tool_version"`
			CreatedAt     string `json:"created_at"`
			Platform      string `json:"platform"`
			ArchiveBytes  int64  `json:"archive_bytes"`
			RequiresZstd  bool   `json:"requires_zstd"`
			ZstdAvailable bool   `json:"zstd_available"`
			Blobs         struct {
				FullCount  int   `json:"full_count"`
				PatchCount int   `json:"patch_count"`
				FullBytes  int64 `json:"full_bytes"`
				PatchBytes int64 `json:"patch_bytes"`
			} `json:"blobs"`
			Images []struct {
				Name                   string `json:"name"`
				BaselineManifestDigest string `json:"baseline_manifest_digest"`
				TargetManifestDigest   string `json:"target_manifest_digest"`
				BaselineProvided       bool   `json:"baseline_provided"`
				WouldImport            bool   `json:"would_import"`
				SkipReason             string `json:"skip_reason"`
				LayerCount             int    `json:"layer_count"`
				ArchiveLayerCount      int    `json:"archive_layer_count"`
				BaselineLayerCount     int    `json:"baseline_layer_count"`
				PatchLayerCount        int    `json:"patch_layer_count"`
			} `json:"images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.Equal(t, "bundle", env.Data.Feature)
	require.Equal(t, "v1", env.Data.Version)
	require.Equal(t, "diffah", env.Data.Tool)
	require.Equal(t, "0.3.0", env.Data.ToolVersion)
	require.Equal(t, "2026-04-23T12:00:00Z", env.Data.CreatedAt)
	require.Equal(t, "linux/amd64", env.Data.Platform)
	require.Equal(t, int64(98765), env.Data.ArchiveBytes)
	require.True(t, env.Data.RequiresZstd)
	require.True(t, env.Data.ZstdAvailable)
	require.Equal(t, 3, env.Data.Blobs.FullCount)
	require.Equal(t, 2, env.Data.Blobs.PatchCount)
	require.Equal(t, int64(50000), env.Data.Blobs.FullBytes)
	require.Equal(t, int64(12000), env.Data.Blobs.PatchBytes)
	require.Len(t, env.Data.Images, 2)

	require.Equal(t, "svc-a", env.Data.Images[0].Name)
	require.Equal(t, "sha256:base-a", env.Data.Images[0].BaselineManifestDigest)
	require.Equal(t, "sha256:target-a", env.Data.Images[0].TargetManifestDigest)
	require.True(t, env.Data.Images[0].BaselineProvided)
	require.True(t, env.Data.Images[0].WouldImport)
	require.Empty(t, env.Data.Images[0].SkipReason)
	require.Equal(t, 5, env.Data.Images[0].LayerCount)
	require.Equal(t, 3, env.Data.Images[0].ArchiveLayerCount)
	require.Equal(t, 2, env.Data.Images[0].BaselineLayerCount)
	require.Equal(t, 1, env.Data.Images[0].PatchLayerCount)

	require.Equal(t, "svc-b", env.Data.Images[1].Name)
	require.False(t, env.Data.Images[1].BaselineProvided)
	require.False(t, env.Data.Images[1].WouldImport)
	require.Equal(t, "no baseline provided", env.Data.Images[1].SkipReason)
}

func TestImportDryRunJSON_NoSkipReason(t *testing.T) {
	report := importer.DryRunReport{
		Feature:     "bundle",
		Version:     "v1",
		Tool:        "diffah",
		ToolVersion: "0.3.0",
		CreatedAt:   time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []importer.ImageDryRun{
			{
				Name:                   "svc",
				BaselineManifestDigest: digest.Digest("sha256:aa"),
				TargetManifestDigest:   digest.Digest("sha256:bb"),
				BaselineProvided:       true,
				WouldImport:            true,
				LayerCount:             3,
				ArchiveLayerCount:      2,
				BaselineLayerCount:     1,
				PatchLayerCount:        0,
			},
		},
		Blobs: importer.BlobStats{
			FullCount: 2, PatchCount: 0, FullBytes: 100, PatchBytes: 0,
		},
	}

	result := importDryRunJSON(report)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	raw := buf.String()
	require.Contains(t, raw, `"schema_version": 1`)
	require.NotContains(t, raw, "skip_reason", "skip_reason should be omitted when empty")
}
