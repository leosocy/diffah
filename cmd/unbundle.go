package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

var unbundleFlags = struct {
	imageFormat  string
	allowConvert bool
	strict       bool
	dryRun       bool
}{}

const unbundleExample = `  # Reconstruct all images from a bundle using a baseline spec
  diffah unbundle bundle.tar baselines.json ./restored/

  # Strict mode — fail if any baseline referenced by the bundle is missing
  diffah unbundle --strict bundle.tar baselines.json ./restored/

  # Write reconstructed images as directories instead of tars
  diffah unbundle --image-format dir bundle.tar baselines.json ./restored/`

func newUnbundleCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR",
		Short: "Reconstruct all images from a multi-image delta bundle.",
		Args: requireArgs("unbundle",
			[]string{"DELTA-IN", "BASELINE-SPEC", "OUTPUT-DIR"},
			"diffah unbundle bundle.tar baselines.json ./restored/"),
		Example: unbundleExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN        path to the bundle archive produced by 'diffah bundle'\n" +
				"  BASELINE-SPEC   JSON spec mapping image name -> baseline path\n" +
				"  OUTPUT-DIR      directory where reconstructed images are written",
		},
		RunE: runUnbundle,
	}
	f := c.Flags()
	f.StringVar(&unbundleFlags.imageFormat, "image-format", "",
		"reconstructed image format: docker-archive|oci-archive|dir (default: match baseline)")
	f.BoolVar(&unbundleFlags.allowConvert, "allow-convert", false, "allow format conversion")
	f.BoolVar(&unbundleFlags.strict, "strict", false, "require every baseline referenced by the bundle")
	f.BoolVarP(&unbundleFlags.dryRun, "dry-run", "n", false, "verify reachability without writing")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newUnbundleCommand()) }

func runUnbundle(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	specPath := args[1]
	outDir := args[2]

	spec, err := diff.ParseBaselineSpec(specPath)
	if err != nil {
		return fmt.Errorf("parse baseline spec: %w", err)
	}

	// Build Outputs map: per-image output refs derived from OUTPUT-DIR and --image-format.
	// Each image writes to <outDir>/<name>.tar (archive) or <outDir>/<name>/ (dir).
	// Stage 5.4 will rewire this to accept transport-prefixed OUTPUT-SPEC directly.
	outputs := make(map[string]string, len(spec.Baselines))
	for name := range spec.Baselines {
		switch unbundleFlags.imageFormat {
		case importer.FormatDir:
			outputs[name] = "dir:" + filepath.Join(outDir, name)
		case importer.FormatDockerArchive:
			outputs[name] = "docker-archive:" + filepath.Join(outDir, name+".tar")
		default:
			// Default: oci-archive (also covers "oci-archive" explicit value)
			outputs[name] = "oci-archive:" + filepath.Join(outDir, name+".tar")
		}
	}

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        spec.Baselines, // raw transport-prefixed refs from the spec
		Outputs:          outputs,
		Strict:           unbundleFlags.strict,
		AllowConvert:     unbundleFlags.allowConvert,
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
	fmt.Fprintf(cmd.OutOrStdout(), "wrote images to %s\n", outDir)
	return nil
}
