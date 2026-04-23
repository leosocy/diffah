package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/exporter"
)

func TestExportDryRunJSON_Structure(t *testing.T) {
	stats := exporter.DryRunStats{
		TotalBlobs:  7,
		TotalImages: 2,
		ArchiveSize: 123456,
		PerImage: []exporter.ImageStats{
			{Name: "svc-a", ShippedBlobs: 4, ArchiveSize: 80000},
			{Name: "svc-b", ShippedBlobs: 3, ArchiveSize: 43456},
		},
	}

	result := exportDryRunJSON(stats)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			TotalBlobs   int   `json:"total_blobs"`
			TotalImages  int   `json:"total_images"`
			ArchiveBytes int64 `json:"archive_bytes"`
			Images       []struct {
				Name         string `json:"name"`
				ShippedBlobs int    `json:"shipped_blobs"`
				ArchiveBytes int64  `json:"archive_bytes"`
			} `json:"images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.Equal(t, 7, env.Data.TotalBlobs)
	require.Equal(t, 2, env.Data.TotalImages)
	require.Equal(t, int64(123456), env.Data.ArchiveBytes)
	require.Len(t, env.Data.Images, 2)
	require.Equal(t, "svc-a", env.Data.Images[0].Name)
	require.Equal(t, 4, env.Data.Images[0].ShippedBlobs)
	require.Equal(t, int64(80000), env.Data.Images[0].ArchiveBytes)
	require.Equal(t, "svc-b", env.Data.Images[1].Name)
	require.Equal(t, 3, env.Data.Images[1].ShippedBlobs)
	require.Equal(t, int64(43456), env.Data.Images[1].ArchiveBytes)
}

func TestExportDryRunJSON_EmptyPerImage(t *testing.T) {
	stats := exporter.DryRunStats{
		TotalBlobs:  0,
		TotalImages: 0,
		ArchiveSize: 0,
		PerImage:    nil,
	}

	result := exportDryRunJSON(stats)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			TotalBlobs   int   `json:"total_blobs"`
			TotalImages  int   `json:"total_images"`
			ArchiveBytes int64 `json:"archive_bytes"`
			Images       []any `json:"images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.Empty(t, env.Data.Images)
}
