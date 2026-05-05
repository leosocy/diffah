package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/importer"
)

var applyFlags = struct {
	allowConvert       bool
	dryRun             bool
	buildSystemContext registryContextBuilder
	buildVerify        verifyConfigBuilder
	buildImportSpool   importSpoolOptsBuilder
}{}

const applyExample = `  # Registry round-trip
  diffah apply delta.tar docker://ghcr.io/org/app:v1 docker://ghcr.io/org/app:v2

  # Registry baseline -> local OCI archive
  diffah apply delta.tar docker://ghcr.io/org/app:v1 oci-archive:/tmp/out.tar

  # Local archive baseline -> registry push
  diffah apply delta.tar docker-archive:/tmp/old.tar docker://harbor/app:v2`

func newApplyCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply DELTA-IN BASELINE-IMAGE TARGET-IMAGE",
		Short: "Reconstruct a single image from a delta archive and a baseline.",
		Long: `Reconstruct a single image from a delta archive and a baseline.

` + importSpoolHelp,
		Args: requireArgs("apply",
			[]string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-IMAGE"},
			"diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar"),
		Example: applyExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN         path to the delta archive produced by 'diffah diff'\n" +
				"  BASELINE-IMAGE   image to apply the delta on top of (transport:path)\n" +
				"  TARGET-IMAGE     where to write the reconstructed image (transport:path)",
		},
		RunE: runApply,
	}
	f := c.Flags()
	f.BoolVar(&applyFlags.allowConvert, "allow-convert", false, "allow format conversion during apply")
	f.BoolVarP(&applyFlags.dryRun, "dry-run", "n", false, "verify baseline reachability without writing")
	applyFlags.buildSystemContext = installRegistryFlags(c)
	applyFlags.buildVerify = installVerifyFlags(c)
	applyFlags.buildImportSpool = installImportSpoolFlags(c)
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
	target, err := ParseImageRef("TARGET-IMAGE", args[2])
	if err != nil {
		return err
	}

	opts, err := buildApplyOptions(cmd, deltaIn, baseline, target)
	if err != nil {
		return err
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

	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", target.Raw)
	return nil
}

func buildApplyOptions(cmd *cobra.Command, deltaIn string, baseline, target ImageRef) (importer.Options, error) {
	sc, retryTimes, retryDelay, err := applyFlags.buildSystemContext()
	if err != nil {
		return importer.Options{}, err
	}
	vc, err := applyFlags.buildVerify()
	if err != nil {
		return importer.Options{}, err
	}
	imp, err := applyFlags.buildImportSpool()
	if err != nil {
		return importer.Options{}, err
	}

	return importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        map[string]string{"default": baseline.Raw},
		Outputs:          map[string]string{"default": target.Raw},
		Strict:           true,
		AllowConvert:     applyFlags.allowConvert,
		SystemContext:    sc,
		RetryTimes:       retryTimes,
		RetryDelay:       retryDelay,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
		VerifyPubKeyPath: vc.PubKeyPath,
		VerifyRekorURL:   vc.RekorURL,
		// Streaming I/O knobs (plumbing only for PR1; consumed in PR3-PR5).
		// Workers > 1 is accepted silently on apply (single-image path)
		// for CLI symmetry; PR5 activates it on the unbundle path.
		Workdir:      imp.Workdir,
		MemoryBudget: imp.MemoryBudget,
		Workers:      imp.Workers,
	}, nil
}
