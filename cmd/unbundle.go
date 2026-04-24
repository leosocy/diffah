package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

var unbundleFlags = struct {
	allowConvert       bool
	strict             bool
	dryRun             bool
	buildSystemContext registryContextBuilder
}{}

const unbundleExample = `  # Multi-image registry round-trip
  diffah unbundle bundle.tar baselines.json outputs.json

  # Mixed-destination (registry + local tar)
  diffah unbundle --strict bundle.tar baselines.json outputs.json`

func newUnbundleCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "unbundle DELTA-IN BASELINE-SPEC OUTPUT-SPEC",
		Short: "Reconstruct all images from a multi-image delta bundle.",
		Args: requireArgs("unbundle",
			[]string{"DELTA-IN", "BASELINE-SPEC", "OUTPUT-SPEC"},
			"diffah unbundle bundle.tar baselines.json outputs.json"),
		Example: unbundleExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN        path to the bundle archive produced by 'diffah bundle'\n" +
				"  BASELINE-SPEC   JSON spec mapping image name -> baseline image reference\n" +
				"  OUTPUT-SPEC     JSON spec mapping image name -> output image reference",
		},
		RunE: runUnbundle,
	}
	f := c.Flags()
	f.BoolVar(&unbundleFlags.allowConvert, "allow-convert", false, "allow format conversion")
	f.BoolVar(&unbundleFlags.strict, "strict", false, "require every baseline referenced by the bundle")
	f.BoolVarP(&unbundleFlags.dryRun, "dry-run", "n", false, "verify reachability without writing")
	unbundleFlags.buildSystemContext = installRegistryFlags(c)
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newUnbundleCommand()) }

func runUnbundle(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	specPath := args[1]
	outputSpecPath := args[2]

	spec, err := diff.ParseBaselineSpec(specPath)
	if err != nil {
		return fmt.Errorf("parse baseline spec: %w", err)
	}

	outputSpec, err := diff.ParseOutputSpec(outputSpecPath)
	if err != nil {
		return fmt.Errorf("parse output spec: %w", err)
	}

	sc, retryTimes, retryDelay, err := unbundleFlags.buildSystemContext()
	if err != nil {
		return err
	}

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        spec.Baselines,
		Outputs:          outputSpec.Outputs,
		Strict:           unbundleFlags.strict,
		AllowConvert:     unbundleFlags.allowConvert,
		SystemContext:    sc,
		RetryTimes:       retryTimes,
		RetryDelay:       retryDelay,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if unbundleFlags.dryRun {
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
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %d images per %s\n", len(outputSpec.Outputs), outputSpecPath)
	return nil
}
