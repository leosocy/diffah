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

	result := inspectJSON("/tmp/bundle.tar", s, true, true)

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

	result := inspectJSON("/tmp/bundle.tar", s, false, true)

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

	raw, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)

	s, err := diff.ParseSidecar(raw)
	require.NoError(t, err)

	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(t.Context())

	result := inspectJSON(archivePath, s, requiresZstd, zstdAvailable)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	got := buf.String()

	snap := filepath.Join("testdata", "schemas", "inspect.snap.json")
	want, err := os.ReadFile(snap)
	if err != nil {
		if os.Getenv("DIFFAH_UPDATE_SNAPSHOTS") == "1" {
			require.NoError(t, os.MkdirAll(filepath.Dir(snap), 0o755))
			require.NoError(t, os.WriteFile(snap, []byte(normalizeJSON(got)), 0o644))
			return
		}
		t.Fatalf("snapshot missing; rerun with DIFFAH_UPDATE_SNAPSHOTS=1: %v", err)
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
