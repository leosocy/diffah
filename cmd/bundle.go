package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

var bundleFlags = struct {
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

const bundleExample = `  # Bundle multiple images using a spec file
  diffah bundle bundle.json bundle.tar

  # Dry-run (plan only)
  diffah bundle --dry-run bundle.json bundle.tar`

func newBundleCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "bundle BUNDLE-SPEC DELTA-OUT",
		Short: "Export a multi-image delta bundle driven by a spec file.",
		Args: requireArgs("bundle",
			[]string{"BUNDLE-SPEC", "DELTA-OUT"},
			"diffah bundle bundle.json bundle.tar"),
		Example: bundleExample,
		Annotations: map[string]string{
			"arguments": "  BUNDLE-SPEC   JSON spec listing per-image {name, baseline, target} triples\n" +
				"  DELTA-OUT     filesystem path to write the multi-image delta archive",
		},
		RunE: runBundle,
	}
	f := c.Flags()
	f.StringVar(&bundleFlags.platform, "platform", "linux/amd64", "target platform")
	f.StringVar(&bundleFlags.compress, "compress", "", "compression algorithm")
	f.StringVar(&bundleFlags.intraLayer, "intra-layer", "auto", "intra-layer diff mode (auto|off|required)")
	f.BoolVarP(&bundleFlags.dryRun, "dry-run", "n", false, "plan without writing the bundle")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newBundleCommand()) }

func runBundle(cmd *cobra.Command, args []string) error {
	specPath := args[0]
	deltaOut := args[1]

	spec, err := diff.ParseBundleSpec(specPath)
	if err != nil {
		return fmt.Errorf("parse bundle spec: %w", err)
	}
	pairs := make([]exporter.Pair, len(spec.Pairs))
	for i, p := range spec.Pairs {
		pairs[i] = exporter.Pair{
			Name:         p.Name,
			BaselinePath: p.Baseline,
			TargetPath:   p.Target,
		}
	}

	opts := exporter.Options{
		Pairs:            pairs,
		Platform:         bundleFlags.platform,
		Compress:         bundleFlags.compress,
		IntraLayer:       bundleFlags.intraLayer,
		OutputPath:       deltaOut,
		ToolVersion:      version,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if bundleFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), exportDryRunJSON(stats))
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"bundle would ship %d blobs across %d images (%d bytes archive)\n",
			stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", deltaOut)
	return nil
}
