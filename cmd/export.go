package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/exporter"
)

var exportFlags = struct {
	target           string
	baseline         string
	baselineManifest string
	platform         string
	compress         string
	intraLayer       string
	output           string
	dryRun           bool
}{}

func newExportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "export",
		Short: "Export a layer-diff delta archive from baseline and target images.",
		RunE:  runExport,
	}
	f := c.Flags()
	f.StringVar(&exportFlags.target, "target", "", "target image reference (required)")
	f.StringVar(&exportFlags.baseline, "baseline", "", "baseline image reference")
	f.StringVar(&exportFlags.baselineManifest, "baseline-manifest", "",
		"path to a baseline manifest.json (alternative to --baseline)")
	f.StringVar(&exportFlags.platform, "platform", "", "os/arch[/variant] (required for manifest lists)")
	f.StringVar(&exportFlags.compress, "compress", "none", "outer compression: none|zstd")
	f.StringVar(&exportFlags.intraLayer, "intra-layer", "auto",
		"per-layer binary patching: auto|off (default auto)")
	f.StringVar(&exportFlags.output, "output", "", "output delta archive path (required)")
	f.BoolVar(&exportFlags.dryRun, "dry-run", false, "compute the plan without writing output")
	_ = c.MarkFlagRequired("target")
	_ = c.MarkFlagRequired("output")
	return c
}

func init() {
	rootCmd.AddCommand(newExportCommand())
}

func runExport(cmd *cobra.Command, _ []string) error {
	if exportFlags.baseline == "" && exportFlags.baselineManifest == "" {
		return errors.New("one of --baseline or --baseline-manifest is required")
	}
	if exportFlags.baseline != "" && exportFlags.baselineManifest != "" {
		return errors.New("--baseline and --baseline-manifest are mutually exclusive")
	}

	targetRef, err := imageio.ParseReference(exportFlags.target)
	if err != nil {
		return err
	}
	opts := exporter.Options{
		TargetRef:            targetRef,
		Platform:             exportFlags.platform,
		Compress:             exportFlags.compress,
		IntraLayer:           exportFlags.intraLayer,
		OutputPath:           exportFlags.output,
		BaselineManifestPath: exportFlags.baselineManifest,
		ToolVersion:          version,
	}
	if exportFlags.baseline != "" {
		opts.BaselineRef, err = imageio.ParseReference(exportFlags.baseline)
		if err != nil {
			return err
		}
	}

	ctx := context.Background()
	if exportFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"delta would ship %d blobs (%d bytes); require %d blobs (%d bytes) from baseline\n",
			stats.ShippedCount, stats.ShippedBytes, stats.RequiredCount, stats.RequiredBytes)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", exportFlags.output)
	return nil
}
