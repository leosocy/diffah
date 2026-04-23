package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func exportDryRunJSON(stats exporter.DryRunStats) any {
	images := make([]map[string]any, 0, len(stats.PerImage))
	for _, img := range stats.PerImage {
		images = append(images, map[string]any{
			"name":          img.Name,
			"shipped_blobs": img.ShippedBlobs,
			"archive_bytes": img.ArchiveSize,
		})
	}
	return map[string]any{
		"total_blobs":   stats.TotalBlobs,
		"total_images":  stats.TotalImages,
		"archive_bytes": stats.ArchiveSize,
		"images":        images,
	}
}

func importDryRunJSON(r importer.DryRunReport) any {
	images := make([]map[string]any, 0, len(r.Images))
	for _, img := range r.Images {
		entry := map[string]any{
			"name":                     img.Name,
			"baseline_manifest_digest": img.BaselineManifestDigest.String(),
			"target_manifest_digest":   img.TargetManifestDigest.String(),
			"baseline_provided":        img.BaselineProvided,
			"would_import":             img.WouldImport,
			"layer_count":              img.LayerCount,
			"archive_layer_count":      img.ArchiveLayerCount,
			"baseline_layer_count":     img.BaselineLayerCount,
			"patch_layer_count":        img.PatchLayerCount,
		}
		if img.SkipReason != "" {
			entry["skip_reason"] = img.SkipReason
		}
		images = append(images, entry)
	}
	return map[string]any{
		"feature":      r.Feature,
		"version":      r.Version,
		"tool":         r.Tool,
		"tool_version": r.ToolVersion,
		"created_at":   r.CreatedAt.UTC().Format(time.RFC3339),
		"platform":     r.Platform,
		"images":       images,
		"blobs": map[string]any{
			"full_count":  r.Blobs.FullCount,
			"patch_count": r.Blobs.PatchCount,
			"full_bytes":  r.Blobs.FullBytes,
			"patch_bytes": r.Blobs.PatchBytes,
		},
		"archive_bytes":  r.ArchiveBytes,
		"requires_zstd":  r.RequiresZstd,
		"zstd_available": r.ZstdAvailable,
	}
}

func renderDryRunReport(w io.Writer, r importer.DryRunReport) error {
	fmt.Fprintf(w, "archive: feature=%s version=%s platform=%s\n",
		r.Feature, r.Version, r.Platform)
	fmt.Fprintf(w, "tool: %s %s, created %s\n",
		r.Tool, r.ToolVersion, r.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "archive bytes: %d\n", r.ArchiveBytes)
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d) — full: %d B, patch: %d B\n",
		r.Blobs.FullCount+r.Blobs.PatchCount,
		r.Blobs.FullCount, r.Blobs.PatchCount,
		r.Blobs.FullBytes, r.Blobs.PatchBytes)
	fmt.Fprintf(w, "images: %d\n", len(r.Images))
	for _, img := range r.Images {
		state := "would import"
		if !img.WouldImport {
			state = fmt.Sprintf("skip — %s", img.SkipReason)
		}
		fmt.Fprintf(w, "  %-20s target=%s (%s)\n", img.Name, img.TargetManifestDigest, state)
		fmt.Fprintf(w, "    layers: %d total — %d shipped, %d from baseline, %d patched\n",
			img.LayerCount, img.ArchiveLayerCount, img.BaselineLayerCount, img.PatchLayerCount)
	}
	return nil
}
