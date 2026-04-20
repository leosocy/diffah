package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/importer"
)

var importFlags = struct {
	delta        string
	baseline     string
	outputFormat string
	output       string
	dryRun       bool
	allowConvert bool
}{}

func newImportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "import",
		Short: "Reconstruct a full image from a delta archive and a baseline source.",
		RunE:  runImport,
	}
	f := c.Flags()
	f.StringVar(&importFlags.delta, "delta", "", "delta archive path (required)")
	f.StringVar(&importFlags.baseline, "baseline", "", "baseline image reference (required)")
	f.StringVar(&importFlags.outputFormat, "output-format", "",
		"auto (default, preserves source format)|docker-archive|oci-archive|dir")
	f.StringVar(&importFlags.output, "output", "", "output path (required)")
	f.BoolVar(&importFlags.dryRun, "dry-run", false, "verify baseline reachability only (no copy)")
	f.BoolVar(&importFlags.allowConvert, "allow-convert", false,
		"allow an --output-format that forces manifest media-type conversion "+
			"(breaks byte-exact reconstruction)")
	_ = c.MarkFlagRequired("delta")
	_ = c.MarkFlagRequired("baseline")
	_ = c.MarkFlagRequired("output")
	return c
}

func init() {
	rootCmd.AddCommand(newImportCommand())
}

func runImport(cmd *cobra.Command, _ []string) error {
	baselineRef, err := imageio.ParseReference(importFlags.baseline)
	if err != nil {
		return err
	}
	opts := importer.Options{
		DeltaPath:    importFlags.delta,
		BaselineRef:  baselineRef,
		OutputFormat: importFlags.outputFormat,
		OutputPath:   importFlags.output,
		AllowConvert: importFlags.allowConvert,
	}
	ctx := context.Background()

	if importFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"required blobs: %d, all reachable: %t\n",
			report.RequiredBlobs, report.AllReachable)
		for _, d := range report.MissingDigests {
			fmt.Fprintf(cmd.ErrOrStderr(), "missing in baseline: %s\n", d)
		}
		if !report.AllReachable {
			return errors.New("baseline missing required blobs")
		}
		return nil
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", importFlags.output)
	return nil
}
