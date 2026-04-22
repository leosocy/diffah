package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

var exportFlags = struct {
	pairs      []string
	bundle     string
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

func newExportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "export --pair NAME=BASELINE,TARGET [--pair ...] OUTPUT",
		Short: "Export a multi-image bundle archive.",
		Args:  cobra.ExactArgs(1),
		RunE:  runExport,
		Example: `  # Single-image bundle
  diffah export --pair app=v1.tar,v2.tar bundle.tar

  # Multi-image bundle
  diffah export --pair svc-a=v1a.tar,v2a.tar --pair svc-b=v1b.tar,v2b.tar bundle.tar

  # Using a bundle spec file
  diffah export --bundle bundle.json bundle.tar

  # Dry run
  diffah export --pair app=v1.tar,v2.tar --dry-run bundle.tar`,
	}
	f := c.Flags()
	f.StringArrayVar(&exportFlags.pairs, "pair", nil, "image pair NAME=BASELINE,TARGET (repeatable)")
	f.StringVar(&exportFlags.bundle, "bundle", "", "path to bundle spec JSON")
	f.StringVar(&exportFlags.platform, "platform", "linux/amd64", "target platform")
	f.StringVar(&exportFlags.compress, "compress", "", "compression algorithm")
	f.StringVar(&exportFlags.intraLayer, "intra-layer", "auto", "intra-layer diff mode (auto|off|required)")
	f.BoolVar(&exportFlags.dryRun, "dry-run", false, "show stats without writing")
	return c
}

func init() {
	rootCmd.AddCommand(newExportCommand())
}

func runExport(cmd *cobra.Command, args []string) error {
	pairs, err := resolveExportPairs()
	if err != nil {
		return err
	}

	opts := exporter.Options{
		Pairs:       pairs,
		Platform:    exportFlags.platform,
		Compress:    exportFlags.compress,
		IntraLayer:  exportFlags.intraLayer,
		OutputPath:  args[0],
		ToolVersion: version,
	}

	ctx := context.Background()
	if exportFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"delta would ship %d blobs across %d images (%d bytes archive)\n",
			stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", args[0])
	return nil
}

func resolveExportPairs() ([]exporter.Pair, error) {
	hasPair := len(exportFlags.pairs) > 0
	hasBundle := exportFlags.bundle != ""

	if hasPair && hasBundle {
		return nil, fmt.Errorf("--pair and --bundle are mutually exclusive")
	}
	if !hasPair && !hasBundle {
		return nil, fmt.Errorf("exactly one of --pair or --bundle is required")
	}

	if hasBundle {
		spec, err := diff.ParseBundleSpec(exportFlags.bundle)
		if err != nil {
			return nil, fmt.Errorf("parse bundle spec: %w", err)
		}
		pairs := make([]exporter.Pair, len(spec.Pairs))
		for i, p := range spec.Pairs {
			pairs[i] = exporter.Pair{
				Name:         p.Name,
				BaselinePath: p.Baseline,
				TargetPath:   p.Target,
			}
		}
		return pairs, nil
	}

	pairs := make([]exporter.Pair, 0, len(exportFlags.pairs))
	for _, raw := range exportFlags.pairs {
		p, err := parsePairFlag(raw)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

func parsePairFlag(raw string) (exporter.Pair, error) {
	eqIdx := strings.Index(raw, "=")
	if eqIdx < 1 {
		return exporter.Pair{}, fmt.Errorf("invalid --pair %q: expected NAME=BASELINE,TARGET", raw)
	}
	name := raw[:eqIdx]
	rest := raw[eqIdx+1:]
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return exporter.Pair{}, fmt.Errorf("invalid --pair %q: expected NAME=BASELINE,TARGET", raw)
	}
	return exporter.Pair{
		Name:         name,
		BaselinePath: parts[0],
		TargetPath:   parts[1],
	}, nil
}
