package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

func newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <delta-archive>",
		Short: "Print sidecar metadata and size statistics from a delta archive.",
		Args:  cobra.ExactArgs(1),
		RunE:  runInspect,
	}
}

func init() {
	rootCmd.AddCommand(newInspectCommand())
}

func runInspect(cmd *cobra.Command, args []string) error {
	raw, err := archive.ReadSidecar(args[0])
	if err != nil {
		return err
	}
	s, err := diff.ParseSidecar(raw)
	if err != nil {
		return err
	}
	return printSidecar(cmd.OutOrStdout(), args[0], s)
}

func printSidecar(w io.Writer, path string, s *diff.Sidecar) error {
	var shipped, required int64
	for _, b := range s.ShippedInDelta {
		shipped += b.Size
	}
	for _, b := range s.RequiredFromBaseline {
		required += b.Size
	}
	total := shipped + required
	var savedPct float64
	if total > 0 {
		savedPct = (float64(required) / float64(total)) * 100
	}
	fmt.Fprintf(w, "archive: %s\n", path)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	fmt.Fprintf(w, "platform: %s\n", s.Platform)
	fmt.Fprintf(w, "target manifest digest: %s (%s)\n", s.Target.ManifestDigest, s.Target.MediaType)
	fmt.Fprintf(w, "baseline manifest digest: %s (%s)\n", s.Baseline.ManifestDigest, s.Baseline.MediaType)
	fmt.Fprintf(w, "shipped: %d blobs (%d bytes)\n", len(s.ShippedInDelta), shipped)
	fmt.Fprintf(w, "required: %d blobs (%d bytes)\n", len(s.RequiredFromBaseline), required)
	fmt.Fprintf(w, "saved %.1f%% vs full image\n", savedPct)
	return nil
}
