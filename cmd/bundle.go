package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

// defaultToArchiveTransport normalises a BundleSpec baseline/target value
// into an alltransports-parseable reference. Already-prefixed values
// ("docker://…", "oci-archive:…", "docker-archive:…") pass through
// unchanged. Bare paths are sniffed against the tar header: an OCI
// archive gets "oci-archive:" and a docker archive gets "docker-archive:".
// If the file cannot be opened or the format cannot be determined the
// function falls back to "docker-archive:" so the downstream
// alltransports parser emits the error message that existed pre-Phase 3.
// Phase 5 will retire this shim by requiring transport prefixes at the
// spec-parse layer.
//
// Transport-prefix detection uses the same heuristic the shim has
// always used: a leading segment before ':' that contains no slash or
// backslash is treated as a transport name. BundleSpec values are
// POSIX-style absolute paths today; Windows support would have to
// revisit this before the shim is retired.
func defaultToArchiveTransport(s string) string {
	if hasTransportPrefix(s) {
		return s
	}
	if format, err := imageio.SniffArchiveFormat(s); err == nil {
		return format + ":" + s
	}
	return imageio.FormatDockerArchive + ":" + s
}

func hasTransportPrefix(s string) bool {
	i := strings.Index(s, ":")
	if i <= 0 {
		return false
	}
	prefix := s[:i]
	return !strings.ContainsAny(prefix, "/\\")
}

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
			Name:        p.Name,
			BaselineRef: defaultToArchiveTransport(p.Baseline),
			TargetRef:   defaultToArchiveTransport(p.Target),
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
