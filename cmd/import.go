package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

var importFlags = struct {
	baselines    []string
	baselineSpec string
	strict       bool
	imageFormat  string
	allowConvert bool
	dryRun       bool
}{}

func newImportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "import --baseline NAME=PATH [--baseline ...] DELTA OUTPUT",
		Short: "Import a multi-image bundle archive.",
		Args:  cobra.ExactArgs(2),
		RunE:  runImport,
		Example: `  # Single-image import (positional baseline)
  diffah import --baseline default=v1.tar bundle.tar output.tar

  # Multi-image import with named baselines
  diffah import --baseline svc-a=v1a.tar --baseline svc-b=v1b.tar bundle.tar output.tar

  # Using a baseline spec file
  diffah import --baseline-spec baselines.json bundle.tar output.tar

  # Strict mode
  diffah import --baseline svc-a=v1a.tar --strict bundle.tar output.tar`,
	}
	f := c.Flags()
	f.StringArrayVar(&importFlags.baselines, "baseline", nil, "named baseline NAME=PATH (repeatable)")
	f.StringVar(&importFlags.baselineSpec, "baseline-spec", "", "path to baseline spec JSON")
	f.BoolVar(&importFlags.strict, "strict", false, "require all baselines")
	f.StringVar(&importFlags.imageFormat, "output-format", "", "output image format (oci-archive|docker-archive|dir)")
	f.BoolVar(&importFlags.allowConvert, "allow-convert", false, "allow format conversion")
	f.BoolVar(&importFlags.dryRun, "dry-run", false, "show stats without writing")
	return c
}

func init() {
	rootCmd.AddCommand(newImportCommand())
}

func runImport(cmd *cobra.Command, args []string) error {
	baselines, err := resolveImportBaselines()
	if err != nil {
		return err
	}

	opts := importer.Options{
		DeltaPath:        args[0],
		Baselines:        baselines,
		Strict:           importFlags.strict,
		OutputPath:       args[1],
		OutputFormat:     importFlags.imageFormat,
		AllowConvert:     importFlags.allowConvert,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if importFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
		}
		return renderDryRunReport(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", args[1])
	return nil
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

func resolveImportBaselines() (map[string]string, error) {
	baselines := make(map[string]string)

	for _, raw := range importFlags.baselines {
		name, path, err := parseBaselineFlag(raw)
		if err != nil {
			return nil, err
		}
		baselines[name] = path
	}

	if importFlags.baselineSpec != "" {
		spec, err := diff.ParseBaselineSpec(importFlags.baselineSpec)
		if err != nil {
			return nil, fmt.Errorf("parse baseline spec: %w", err)
		}
		for name, path := range spec.Baselines {
			baselines[name] = path
		}
	}

	if len(baselines) == 0 {
		return nil, fmt.Errorf("at least one --baseline or --baseline-spec is required")
	}
	return baselines, nil
}

func parseBaselineFlag(raw string) (string, string, error) {
	eqIdx := strings.Index(raw, "=")
	if eqIdx < 1 {
		return "", "", fmt.Errorf("invalid --baseline %q: expected NAME=PATH", raw)
	}
	name := raw[:eqIdx]
	path := raw[eqIdx+1:]
	if path == "" {
		return "", "", fmt.Errorf("invalid --baseline %q: expected NAME=PATH", raw)
	}
	return name, path, nil
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
	result := map[string]any{
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
	return result
}
