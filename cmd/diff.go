package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/exporter"
)

var diffFlags = struct {
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

const diffExample = `  # Compute a single-image delta
  diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar

  # Cross-format (oci-archive baseline, docker-archive target)
  diffah diff oci-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar

  # Dry-run — plan without writing
  diffah diff --dry-run docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar`

func newDiffCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT",
		Short: "Compute a single-image delta archive.",
		Args: requireArgs("diff",
			[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
			"diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar"),
		Example: diffExample,
		Annotations: map[string]string{
			"arguments": "  BASELINE-IMAGE   older image to diff against (transport:path; see below)\n" +
				"  TARGET-IMAGE     newer image whose contents become the diff target\n" +
				"  DELTA-OUT        filesystem path to write the delta archive",
		},
		RunE: runDiff,
	}
	f := c.Flags()
	f.StringVar(&diffFlags.platform, "platform", "linux/amd64", "target platform")
	f.StringVar(&diffFlags.compress, "compress", "", "compression algorithm")
	f.StringVar(&diffFlags.intraLayer, "intra-layer", "auto", "intra-layer diff mode (auto|off|required)")
	f.BoolVarP(&diffFlags.dryRun, "dry-run", "n", false, "plan without writing the delta")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newDiffCommand()) }

func runDiff(cmd *cobra.Command, args []string) error {
	baseline, err := ParseImageRef("BASELINE-IMAGE", args[0])
	if err != nil {
		return err
	}
	target, err := ParseImageRef("TARGET-IMAGE", args[1])
	if err != nil {
		return err
	}
	deltaOut := args[2]

	opts := exporter.Options{
		Pairs: []exporter.Pair{{
			Name:        "default",
			BaselineRef: baseline.Path, // still bare path — perpair's OpenArchiveRef expects this
			TargetRef:   target.Path,
		}},
		Platform:         diffFlags.platform,
		Compress:         diffFlags.compress,
		IntraLayer:       diffFlags.intraLayer,
		OutputPath:       deltaOut,
		ToolVersion:      version,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}

	ctx := context.Background()
	if diffFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), exportDryRunJSON(stats))
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"delta would ship %d blobs across %d images (%d bytes archive)\n",
			stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", deltaOut)
	return nil
}
