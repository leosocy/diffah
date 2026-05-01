package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

func TestInspectJSON_Structure(t *testing.T) {
	s := &diff.Sidecar{
		Version:     "v1",
		Feature:     "bundle",
		Tool:        "diffah",
		ToolVersion: "v0.3.0",
		CreatedAt:   time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
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
		},
		Blobs: map[digest.Digest]diff.BlobEntry{
			"sha256:manifest-a": {Size: 200, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 200},
			"sha256:layer1":     {Size: 5000, MediaType: "m", Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: "sha256:ref1", ArchiveSize: 1000},
			"sha256:layer2":     {Size: 3000, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 3000},
		},
	}

	result := inspectJSON("/tmp/bundle.tar", s, true, true, nil)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Archive           string `json:"archive"`
			Version           string `json:"version"`
			Feature           string `json:"feature"`
			Tool              string `json:"tool"`
			ToolVersion       string `json:"tool_version"`
			Platform          string `json:"platform"`
			CreatedAt         string `json:"created_at"`
			RequiresZstd      bool   `json:"requires_zstd"`
			ZstdAvailable     bool   `json:"zstd_available"`
			TotalArchiveBytes int64  `json:"total_archive_bytes"`
			Blobs             struct {
				Total      int   `json:"total"`
				FullCount  int   `json:"full_count"`
				PatchCount int   `json:"patch_count"`
				FullBytes  int64 `json:"full_bytes"`
				PatchBytes int64 `json:"patch_bytes"`
			} `json:"blobs"`
			Images []struct {
				Name   string `json:"name"`
				Target struct {
					ManifestDigest string `json:"manifest_digest"`
					MediaType      string `json:"media_type"`
				} `json:"target"`
				Baseline struct {
					ManifestDigest string `json:"manifest_digest"`
					MediaType      string `json:"media_type"`
					SourceHint     string `json:"source_hint"`
				} `json:"baseline"`
			} `json:"images"`
			PatchSavings struct {
				Bytes int64   `json:"bytes"`
				Ratio float64 `json:"ratio"`
			} `json:"patch_savings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.Equal(t, "/tmp/bundle.tar", env.Data.Archive)
	require.Equal(t, "v1", env.Data.Version)
	require.Equal(t, "bundle", env.Data.Feature)
	require.Equal(t, "diffah", env.Data.Tool)
	require.Equal(t, "v0.3.0", env.Data.ToolVersion)
	require.Equal(t, "linux/amd64", env.Data.Platform)
	require.True(t, env.Data.RequiresZstd)
	require.True(t, env.Data.ZstdAvailable)
	require.Equal(t, int64(4200), env.Data.TotalArchiveBytes)
	require.Equal(t, 3, env.Data.Blobs.Total)
	require.Equal(t, 2, env.Data.Blobs.FullCount)
	require.Equal(t, 1, env.Data.Blobs.PatchCount)
	require.Len(t, env.Data.Images, 1)
	require.Equal(t, "service-a", env.Data.Images[0].Name)
	require.Equal(t, "sha256:manifest-a", env.Data.Images[0].Target.ManifestDigest)
	require.Equal(t, "sha256:base-a", env.Data.Images[0].Baseline.ManifestDigest)
	require.Equal(t, "oci-archive:/tmp/base-a.tar", env.Data.Images[0].Baseline.SourceHint)
	require.Equal(t, int64(4000), env.Data.PatchSavings.Bytes)
	require.InDelta(t, 0.8, env.Data.PatchSavings.Ratio, 0.01)
}

func TestInspectJSON_NoPatchSavings(t *testing.T) {
	s := &diff.Sidecar{
		Version:     "v1",
		Feature:     "bundle",
		Tool:        "diffah",
		ToolVersion: "v0.3.0",
		CreatedAt:   time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{
			{
				Name: "svc",
				Target: diff.TargetRef{
					ManifestDigest: "sha256:aa",
					ManifestSize:   5,
					MediaType:      "m",
				},
				Baseline: diff.BaselineRef{
					ManifestDigest: "sha256:bb",
					MediaType:      "m",
				},
			},
		},
		Blobs: map[digest.Digest]diff.BlobEntry{
			"sha256:aa": {Size: 5, MediaType: "m", Encoding: diff.EncodingFull, ArchiveSize: 5},
		},
	}

	result := inspectJSON("/tmp/bundle.tar", s, false, true, nil)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	raw := buf.String()
	require.Contains(t, raw, `"schema_version": 1`)
	require.NotContains(t, raw, "patch_savings", "no patch_savings when no patches")
}

func TestInspectJSON_Snapshot(t *testing.T) {
	archivePath := filepath.Join("..", "testdata", "fixtures", "v5_bundle.tar")
	if _, err := os.Stat(archivePath); err != nil {
		t.Skipf("fixture missing: %s", archivePath)
	}

	rawSidecar, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)
	s, err := diff.ParseSidecar(rawSidecar)
	require.NoError(t, err)

	digests := make([]digest.Digest, 0, len(s.Images))
	for _, img := range s.Images {
		digests = append(digests, img.Target.ManifestDigest)
	}
	_, blobs, err := archive.ReadSidecarAndManifestBlobs(archivePath, digests)
	require.NoError(t, err)

	details := make(map[string]importer.InspectImageDetail, len(s.Images))
	for _, img := range s.Images {
		d, derr := importer.BuildInspectImageDetail(s, img, blobs[img.Target.ManifestDigest])
		require.NoError(t, derr)
		details[img.Name] = d
	}

	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(t.Context())

	result := inspectJSON(archivePath, s, requiresZstd, zstdAvailable, details)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	got := buf.String()

	snap := filepath.Join("testdata", "schemas", "inspect.snap.json")
	want, rerr := os.ReadFile(snap)
	if rerr != nil || os.Getenv("DIFFAH_UPDATE_SNAPSHOTS") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(snap), 0o755))
		require.NoError(t, os.WriteFile(snap, []byte(normalizeJSON(got)), 0o644))
		if rerr != nil {
			t.Fatalf("snapshot was missing; written. Re-run to verify.")
		}
		return
	}

	gotNorm := normalizeJSON(got)
	if string(want) != gotNorm {
		t.Errorf("snapshot mismatch.\nwant:\n%s\ngot:\n%s", want, gotNorm)
	}
}

func normalizeJSON(s string) string {
	s = replaceFieldValue(s, "created_at", "<T>")
	s = replaceFieldValue(s, "tool_version", "<V>")
	s = replaceBoolField(s, "zstd_available", "<zstd>")
	return s
}

func replaceFieldValue(s, field, placeholder string) string {
	needle := `"` + field + `": "`
	result := s
	for {
		i := strings.Index(result, needle)
		if i < 0 {
			return result
		}
		start := i + len(needle)
		end := strings.Index(result[start:], `"`)
		if end < 0 {
			return result
		}
		result = result[:start] + placeholder + result[start+end:]
		if !strings.Contains(result[start+len(placeholder):], needle) {
			return result
		}
	}
}

func replaceBoolField(s, field, placeholder string) string {
	needle := `"` + field + `": `
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		valueStart := i + len(needle)
		valueEnd := valueStart
		for valueEnd < len(rest) && rest[valueEnd] != ',' && rest[valueEnd] != '\n' && rest[valueEnd] != '}' {
			valueEnd++
		}
		b.WriteString(rest[:valueStart])
		b.WriteString(`"` + placeholder + `"`)
		rest = rest[valueEnd:]
	}
}

func TestInspectJSON_PerImageDetailKeysPresent(t *testing.T) {
	mfDigest := digest.Digest("sha256:" + strings.Repeat("a", 64))
	s := &diff.Sidecar{
		Version: "v1", Feature: "bundle", Tool: "diffah", ToolVersion: "v0.x",
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC), Platform: "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{ManifestDigest: mfDigest, ManifestSize: 100, MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Baseline: diff.BaselineRef{ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)), MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		},
	}
	details := map[string]importer.InspectImageDetail{
		"svc": {
			Name: "svc", ManifestDigest: mfDigest, LayerCount: 1, ArchiveLayerCount: 1,
			Layers: []importer.LayerRow{
				{Digest: digest.Digest("sha256:" + strings.Repeat("c", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			},
			Histogram: importer.SizeHistogram{
				Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
				Counts:  []int{1, 0, 0, 0, 0},
			},
		},
	}
	result := inspectJSON("/tmp/bundle.tar", s, false, false, details)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		Data struct {
			Images []struct {
				Name              string `json:"name"`
				LayerCount        int    `json:"layer_count"`
				ArchiveLayerCount int    `json:"archive_layer_count"`
				Layers            []struct {
					Digest      string `json:"digest"`
					Encoding    string `json:"encoding"`
					TargetSize  int64  `json:"target_size"`
					ArchiveSize int64  `json:"archive_size"`
				} `json:"layers"`
				Waste         []map[string]any `json:"waste"`
				TopSavings    []map[string]any `json:"top_savings"`
				SizeHistogram struct {
					Buckets []string `json:"buckets"`
					Counts  []int    `json:"counts"`
				} `json:"size_histogram"`
			} `json:"images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Len(t, env.Data.Images, 1)
	img0 := env.Data.Images[0]
	require.Equal(t, "svc", img0.Name)
	require.Equal(t, 1, img0.LayerCount)
	require.Equal(t, 1, img0.ArchiveLayerCount)
	require.Len(t, img0.Layers, 1)
	require.Equal(t, "full", img0.Layers[0].Encoding)
	require.Empty(t, img0.Waste)
}
