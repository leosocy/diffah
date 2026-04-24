package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/importer"
)

var applyFlags = struct {
	imageFormat  string
	allowConvert bool
	dryRun       bool
}{}

const applyExample = `  # Reconstruct a single image from a delta + its baseline
  diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar

  # Write the reconstructed image as a directory (OCI layout)
  diffah apply --image-format dir delta.tar docker-archive:/tmp/old.tar /tmp/restored-dir

  # Dry-run — verify baseline reachability without writing
  diffah apply --dry-run delta.tar docker-archive:/tmp/old.tar /tmp/out.tar`

func newApplyCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply DELTA-IN BASELINE-IMAGE TARGET-OUT",
		Short: "Reconstruct a single image from a delta archive and a baseline.",
		Args: requireArgs("apply",
			[]string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-OUT"},
			"diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar"),
		Example: applyExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN         path to the delta archive produced by 'diffah diff'\n" +
				"  BASELINE-IMAGE   image to apply the delta on top of (transport:path)\n" +
				"  TARGET-OUT       filesystem path to write the reconstructed image",
		},
		RunE: runApply,
	}
	f := c.Flags()
	f.StringVar(&applyFlags.imageFormat, "image-format", "",
		"reconstructed image format: docker-archive|oci-archive|dir (default: match baseline)")
	f.BoolVar(&applyFlags.allowConvert, "allow-convert", false, "allow format conversion during apply")
	f.BoolVarP(&applyFlags.dryRun, "dry-run", "n", false, "verify baseline reachability without writing")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newApplyCommand()) }

func runApply(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	baseline, err := ParseImageRef("BASELINE-IMAGE", args[1])
	if err != nil {
		return err
	}
	targetOut := args[2]

	// TARGET-OUT is the final artifact path. Treat an existing regular file
	// as overwritable (consistent with 'diff'), but refuse to cross-type
	// clobber a directory: that is almost always a user typo.
	if info, err := os.Stat(targetOut); err == nil && info.IsDir() {
		return &cliErr{
			cat:  errs.CategoryUser,
			msg:  fmt.Sprintf("TARGET-OUT %q already exists as a directory; refusing to overwrite", targetOut),
			hint: "remove the directory or pick a different TARGET-OUT path",
		}
	}

	// Determine the output reference. For now, use --image-format to pick the transport;
	// if empty, default to "oci-archive". Stage 5 will rewire TARGET-OUT to require a
	// transport prefix directly.
	outputTransport := applyFlags.imageFormat
	if outputTransport == "" {
		outputTransport = "oci-archive"
	}
	targetRef := outputTransport + ":" + targetOut

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        map[string]string{"default": baseline.Raw},
		Outputs:          map[string]string{"default": targetRef},
		Strict:           true,
		AllowConvert:     applyFlags.allowConvert,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if applyFlags.dryRun {
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

	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", targetOut)
	return nil
}
