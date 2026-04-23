package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

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

	// Importer writes per-image under OutputPath which must be a directory.
	// For single-image apply, stage a scratch dir alongside TARGET-OUT and
	// rename the produced "default.*" artifact to TARGET-OUT.
	scratchParent := filepath.Dir(targetOut)
	if scratchParent == "" {
		scratchParent = "."
	}
	scratch, err := os.MkdirTemp(scratchParent, "diffah-apply-")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        map[string]string{"default": baseline.Path},
		Strict:           true,
		OutputPath:       scratch,
		OutputFormat:     applyFlags.imageFormat,
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

	produced, err := findSingleImageArtifact(scratch)
	if err != nil {
		return err
	}
	if err := os.Rename(produced, targetOut); err != nil {
		return fmt.Errorf("move produced artifact to %s: %w", targetOut, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", targetOut)
	return nil
}

// findSingleImageArtifact returns the path of the single image artifact
// written into the scratch directory by importer.Import. Tolerates archive
// (default.tar) and dir (default/) forms.
func findSingleImageArtifact(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read scratch dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "default" || name == "default.tar" ||
			filepath.Ext(name) == ".tar" {
			return filepath.Join(dir, name), nil
		}
	}
	return "", fmt.Errorf("no default image artifact produced in %s", dir)
}
