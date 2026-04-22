package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

var importFlags = struct {
	baselines    []string
	baselineSpec string
	strict       bool
	outputFormat string
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
	f.StringVar(&importFlags.outputFormat, "output-format", "", "output format (oci-archive|docker-archive|dir)")
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
		DeltaPath:    args[0],
		Baselines:    baselines,
		Strict:       importFlags.strict,
		OutputPath:   args[1],
		OutputFormat: importFlags.outputFormat,
		AllowConvert: importFlags.allowConvert,
	}
	ctx := context.Background()

	if importFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"images: %d, blobs: %d, archive size: %d\n",
			len(report.Images), report.Blobs.FullCount+report.Blobs.PatchCount, report.ArchiveBytes)
		var missing bool
		for _, img := range report.Images {
			if !img.BaselineProvided {
				fmt.Fprintf(cmd.ErrOrStderr(), "missing baseline: %s\n", img.Name)
				missing = true
			}
		}
		if missing {
			return fmt.Errorf("baseline missing required blobs")
		}
		return nil
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", args[1])
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
